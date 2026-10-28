package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func TestMCPProgressAndCancellationWithRealSDK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens := mcpserver.NewTokens(&memoryMCPTokens{})
	_, secret, err := tokens.Create(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	var executions atomic.Int32
	handler, err := mcpserver.New(mcpserver.Options{Tokens: tokens, Enabled: func() bool { return true }, Tools: []mcpserver.Tool{
		{Name: "progress", Schema: json.RawMessage(`{"type":"object"}`), Execute: func(_ context.Context, _ json.RawMessage, progress mcpserver.Progress) (mcpserver.Result, error) {
			if err := progress("开始", 10); err != nil {
				return mcpserver.Result{}, err
			}
			if err := progress("安装完成", 100); err != nil {
				return mcpserver.Result{}, err
			}
			return mcpserver.Result{Data: map[string]any{"completed": true}}, nil
		}},
		{Name: "wait", Schema: json.RawMessage(`{"type":"object"}`), Execute: func(ctx context.Context, _ json.RawMessage, _ mcpserver.Progress) (mcpserver.Result, error) {
			executions.Add(1)
			close(started)
			<-ctx.Done()
			close(stopped)
			return mcpserver.Result{}, ctx.Err()
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	progress := make(chan *mcp.ProgressNotificationParams, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) { progress <- request.Params }})
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: mcpBearerTransport{secret: secret, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	params := &mcp.CallToolParams{Name: "progress", Arguments: map[string]any{}}
	params.SetProgressToken("download")
	result, err := session.CallTool(ctx, params)
	if err != nil || result.IsError {
		t.Fatalf("progress call=%+v %v", result, err)
	}
	for _, pct := range []float64{10, 100} {
		select {
		case event := <-progress:
			if event.Progress != pct || event.ProgressToken != "download" {
				t.Fatalf("event=%+v", event)
			}
		case <-ctx.Done():
			t.Fatal("missing progress notification")
		}
	}
	callCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("tool never started")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("client did not cancel")
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("server business context not cancelled")
	}
	if executions.Load() != 1 {
		t.Fatal("cancelled write retried")
	}
}

func TestMCPRecordingResourcesRequireActiveToken(t *testing.T) {
	tokens := mcpserver.NewTokens(&memoryMCPTokens{})
	token, secret, err := tokens.Create(context.Background(), "recordings")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "call_test.mp3"), []byte("mp3-test"), 0600); err != nil {
		t.Fatal(err)
	}
	server := &Server{mcpTokens: tokens, fullCfg: &config.Config{MCP: config.MCPConfig{Enabled: true}}, voiceRecordingDirectory: directory}
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	handler, err := server.newMCPHandler()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: mcpBearerTransport{secret: secret, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hideck://recordings/call_test.mp3"})
	if err != nil || len(resource.Contents) != 1 || string(resource.Contents[0].Blob) != "mp3-test" {
		t.Fatalf("resource=%+v %v", resource, err)
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hideck://recordings/../../secret"}); err == nil {
		t.Fatal("resource traversal allowed")
	}
	if err := tokens.Revoke(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hideck://recordings/call_test.mp3"}); err == nil {
		t.Fatal("revoked resource read allowed")
	}
}

func TestMCPDelegatesLegacyProtocolVersionsToSDK(t *testing.T) {
	tokens := mcpserver.NewTokens(&memoryMCPTokens{})
	_, secret, err := tokens.Create(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{mcpTokens: tokens, fullCfg: &config.Config{MCP: config.MCPConfig{Enabled: true}}}
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	handler, err := server.newMCPHandler()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"2025-03-26", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			request.Header.Set("Authorization", "Bearer "+secret)
			request.Header.Set("MCP-Protocol-Version", version)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "hideck_sms_send") {
				t.Fatalf("legacy request=%d %s", response.Code, response.Body.String())
			}
		})
	}
}
