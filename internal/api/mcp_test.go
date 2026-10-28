package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

type memoryMCPTokens struct{ token mcpserver.Token }

func (s *memoryMCPTokens) Create(_ context.Context, token mcpserver.Token) error {
	s.token = token
	return nil
}
func (s *memoryMCPTokens) List(context.Context) ([]mcpserver.Token, error) {
	if s.token.ID == "" {
		return []mcpserver.Token{}, nil
	}
	return []mcpserver.Token{s.token}, nil
}
func (s *memoryMCPTokens) FindDigest(_ context.Context, digest string) (mcpserver.Token, error) {
	if s.token.Digest != digest {
		return mcpserver.Token{}, mcpserver.ErrUnauthorized
	}
	return s.token, nil
}
func (s *memoryMCPTokens) FindID(_ context.Context, id string) (mcpserver.Token, error) {
	if s.token.ID != id {
		return mcpserver.Token{}, mcpserver.ErrUnauthorized
	}
	return s.token, nil
}
func (s *memoryMCPTokens) Revoke(_ context.Context, _ string, at time.Time) error {
	s.token.RevokedAt = &at
	return nil
}
func (s *memoryMCPTokens) Delete(_ context.Context, id string) error {
	if s.token.ID != id || id == "" {
		return mcpserver.ErrUnauthorized
	}
	s.token = mcpserver.Token{}
	return nil
}

type mcpBearerTransport struct {
	secret string
	base   http.RoundTripper
}

func (t mcpBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.secret)
	return t.base.RoundTrip(clone)
}

func TestMCPCatalogRealSDK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := &memoryMCPTokens{}
	tokens := mcpserver.NewTokens(store)
	token, secret, err := tokens.Create(ctx, "IDE")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{fullCfg: &config.Config{MCP: config.MCPConfig{Enabled: true}}, mcpTokens: tokens, disclaimerAcceptances: &disclaimerStoreStub{accepted: true}}
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	handler, err := server.newMCPHandler()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: mcpBearerTransport{secret: secret, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) < 115 {
		t.Fatalf("incomplete catalog: %d", len(list.Tools))
	}
	names := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		if names[tool.Name] != nil {
			t.Fatalf("duplicate: %s", tool.Name)
		}
		names[tool.Name] = tool
	}
	for _, name := range []string{"hideck_sms_send", "hideck_esim_switch", "hideck_phone_dial", "hideck_mbn_start", "hideck_task_run", "hideck_contact_import"} {
		if names[name] == nil {
			t.Fatalf("missing %s", name)
		}
	}
	for _, tool := range list.Tools {
		if strings.Contains(tool.Name, "uninstall") || strings.Contains(tool.Name, "password") || strings.Contains(tool.Name, "notification_config") {
			t.Fatalf("forbidden tool: %s", tool.Name)
		}
	}
	if names["hideck_esim_download"].Annotations.ReadOnlyHint || names["hideck_network_scan"].Annotations.ReadOnlyHint {
		t.Fatal("hardware writes incorrectly read-only")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hideck_phone_audio_connections", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("connections: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "hideck_phone_audio_connections"})
	if err != nil || result.IsError {
		t.Fatalf("omitted arguments: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "hideck_phone_dial"})
	if err != nil || !result.IsError {
		t.Fatalf("required arguments were not validated: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "hideck_phone_dial", Arguments: map[string]any{"audio_connection_id": "missing", "request": map[string]any{"device_id": "offline", "callee": "123"}}})
	if err != nil || !result.IsError {
		t.Fatalf("unbound audio accepted: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "hideck_sms_send", Arguments: map[string]any{"request": map[string]any{"phone": "123", "message": "test"}}})
	if err != nil || !result.IsError {
		t.Fatalf("uninitialized device pool: %+v %v", result, err)
	}
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil || len(templates.ResourceTemplates) != 1 {
		t.Fatalf("resources: %+v %v", templates, err)
	}
	if err := tokens.Revoke(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("revoked token still authenticated")
	}
}

func TestMCPAuthenticationAndCrossOrigin(t *testing.T) {
	tokens := mcpserver.NewTokens(&memoryMCPTokens{})
	_, secret, err := tokens.Create(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{fullCfg: &config.Config{MCP: config.MCPConfig{Enabled: true}}, mcpTokens: tokens, disclaimerAcceptances: &disclaimerStoreStub{accepted: true}}
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	handler, err := server.newMCPHandler()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token, origin string
		enabled             bool
		status              int
	}{
		{name: "missing", enabled: true, status: 401}, {name: "invalid", token: "invalid", enabled: true, status: 401},
		{name: "web session", token: testSessionToken(t, "password", time.Now().Add(time.Hour)), enabled: true, status: 401},
		{name: "disabled", token: secret, status: 503}, {name: "cross origin", token: secret, origin: "https://evil.invalid", enabled: true, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server.fullCfg.MCP.Enabled = tc.enabled
			request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMCPResultsPreserveFailuresAndHidePaths(t *testing.T) {
	call := &businessCall{status: 409, result: map[string]any{"code": "MBN_BUSY", "error": "device busy", "audio_path": "/private/call_test.mp3", "pcap_path": "/private/test.pcap"}}
	result, err := mcpBusinessResult(call)
	if err != nil || !result.Failed || len(result.Resources) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, _ := json.Marshal(result.Data)
	if strings.Contains(string(raw), "/private") || !strings.Contains(string(raw), "MBN_BUSY") {
		t.Fatalf("bad output: %s", raw)
	}
	// Legacy log history uses HTTP 200 with an explicit error field. Preserve
	// that response while exposing the failure to MCP clients.
	result, err = mcpBusinessResult(&businessCall{status: 200, result: map[string]any{"error": "无法读取日志文件"}})
	if err != nil || !result.Failed {
		t.Fatalf("legacy business failure hidden: %+v %v", result, err)
	}
}

func TestMCPResultsPreserveDeviceBindingPaths(t *testing.T) {
	paths := map[string]any{"usb_path": "1-2", "pcsc_usb_path": "1-3", "control_path": "/dev/cdc-wdm0", "qmi_proxy_path": "/run/qmi-proxy.sock"}
	result, err := mcpBusinessResult(&businessCall{status: http.StatusOK, result: paths})
	if err != nil || result.Failed {
		t.Fatalf("device result: %+v %v", result, err)
	}
	body := result.Data.(map[string]any)["data"].(map[string]any)
	for key, expected := range paths {
		if body[key] != expected {
			t.Errorf("device binding %s = %v, want %v", key, body[key], expected)
		}
	}
}
