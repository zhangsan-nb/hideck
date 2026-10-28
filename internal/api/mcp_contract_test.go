package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func TestMCPOutputContractsAndInstructionsWithRealSDK(t *testing.T) {
	openTestDB(t)
	session := connectMCPTestClient(t, &Server{})
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	contracts := map[string]*jsonschema.Resolved{}
	for _, tool := range list.Tools {
		if tool.OutputSchema == nil {
			t.Fatalf("missing output schema: %s", tool.Name)
		}
		contracts[tool.Name] = resolveMCPTestOutput(t, tool.OutputSchema)
	}
	instructions := session.InitializeResult().Instructions
	if !strings.Contains(instructions, "202") || !strings.Contains(instructions, "不自动重试") || !strings.Contains(instructions, "401") {
		t.Fatalf("missing workflow/error instructions: %s", instructions)
	}
	for _, name := range regexp.MustCompile(`hideck_[a-z_]+`).FindAllString(instructions, -1) {
		if contracts[name] == nil {
			t.Errorf("instructions reference missing tool: %s", name)
		}
	}
	for _, call := range []struct {
		name   string
		args   map[string]any
		failed bool
	}{
		{name: "hideck_phone_audio_connections"},
		{name: "hideck_contact_list"},
		{name: "hideck_contact_save", args: map[string]any{"request": map[string]any{"number": "10086", "name": "Support"}}},
		{name: "hideck_contact_list"},
		{name: "hideck_contact_export", args: map[string]any{"format": "csv"}},
		{name: "hideck_contact_delete", args: map[string]any{"number": "10086"}},
		{name: "hideck_sms_send", failed: true},
		{name: "hideck_sms_send", args: map[string]any{"request": map[string]any{"phone": "10086", "message": "private test"}}, failed: true},
	} {
		t.Run(call.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: call.name, Arguments: call.args})
			if err != nil || result.IsError != call.failed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if err := contracts[call.name].Validate(result.StructuredContent); err != nil {
				t.Fatal(err)
			}
			var text any
			if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &text); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(result.StructuredContent)
			if string(encoded) != result.Content[0].(*mcp.TextContent).Text {
				t.Fatal("text and structured content differ")
			}
		})
	}
}

func resolveMCPTestOutput(t *testing.T, value any) *jsonschema.Resolved {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestMCPOutputMismatchIsExplicitAndNeverRetries(t *testing.T) {
	tokens := mcpserver.NewTokens(&memoryMCPTokens{})
	_, secret, err := tokens.Create(t.Context(), "contract")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	handler, err := mcpserver.New(mcpserver.Options{Tokens: tokens, Enabled: func() bool { return true }, Tools: []mcpserver.Tool{{
		Name: "write", Schema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`),
		Execute: func(context.Context, json.RawMessage, mcpserver.Progress) (mcpserver.Result, error) {
			calls.Add(1)
			return mcpserver.Result{Data: map[string]any{"id": 1}}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "contract-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: mcpBearerTransport{secret: secret, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "write"})
	if err != nil || !result.IsError || calls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	if !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "操作可能已执行") {
		t.Fatal("write outcome ambiguity hidden")
	}
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolveMCPTestOutput(t, list.Tools[0].OutputSchema).Validate(result.StructuredContent); err != nil {
		t.Fatal(err)
	}
}

func TestMCPOutputPreservesNullableAndSanitizesRecordingSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"recording_path": map[string]any{"type": "string"}, "ended_at": map[string]any{"type": "string", "nullable": true},
	}, "required": []any{"recording_path", "ended_at"}}
	resolved := resolveMCPTestOutput(t, expandMCPResponse(nil, schema))
	if err := resolved.Validate(map[string]any{"ended_at": nil, "recording_uri": "hideck://recordings/call_test.mp3"}); err != nil {
		t.Fatal(err)
	}
	if schema["properties"].(map[string]any)["recording_path"] == nil {
		t.Fatal("source schema mutated")
	}
}

func TestMCPFinishingCallCannotRestoreDeletedTokenActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tokens := mcpserver.NewTokens(&memoryMCPTokens{})
	token, secret, err := tokens.Create(ctx, "in-flight")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	handler, err := mcpserver.New(mcpserver.Options{Tokens: tokens, Enabled: func() bool { return true }, Tools: []mcpserver.Tool{{
		Name: "wait", Schema: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ json.RawMessage, _ mcpserver.Progress) (mcpserver.Result, error) {
			close(started)
			select {
			case <-ctx.Done():
				return mcpserver.Result{}, ctx.Err()
			case <-release:
				return mcpserver.Result{Data: map[string]any{"done": true}}, nil
			}
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "in-flight", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: mcpBearerTransport{secret: secret, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	done := make(chan error, 1)
	go func() { _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "wait"}); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := tokens.Delete(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if tokens.Activity(token.ID) != nil {
		t.Fatal("finishing call restored deleted activity")
	}
}

func TestMCPInvalidOutputSchemaFailsInitialization(t *testing.T) {
	for _, schema := range []string{`not-json`, `{"type":"object","$ref":"#/$defs/missing"}`} {
		_, err := mcpserver.New(mcpserver.Options{Tools: []mcpserver.Tool{{Name: "invalid", Schema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(schema)}}})
		if err == nil || !strings.Contains(err.Error(), "output schema") {
			t.Fatalf("invalid schema accepted: %s, %v", schema, err)
		}
	}
}
