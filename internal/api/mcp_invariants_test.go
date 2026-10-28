package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/device"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func connectMCPTestClient(t *testing.T, server *Server) *mcp.ClientSession {
	t.Helper()
	server.fullCfg = &config.Config{MCP: config.MCPConfig{Enabled: true}}
	server.mcpTokens = mcpserver.NewTokens(&memoryMCPTokens{})
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	server.disclaimerAcceptances = &disclaimerStoreStub{accepted: true}
	_, secret, err := server.mcpTokens.Create(context.Background(), "integration")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.newMCPHandler()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: mcpBearerTransport{secret: secret, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPSharedSMSFailuresWithRealSDK(t *testing.T) {
	openTestDB(t)
	pool := device.NewPool(&config.Config{})
	worker := &device.Worker{ID: "dev", Backend: &ussdDeviceBackendStub{}}
	setNestedPrivateField(t, worker, []string{"state", "Identity", "ICCID"}, "8944000000000000601")
	setNestedPrivateField(t, worker, []string{"state", "Identity", "IMSI"}, "234150000000601")
	pool.AttachWorkerForTest(worker)
	pool.SetOutboundLimiter(deniedOutbound{})
	var sends atomic.Int32
	pool.SetRoutedSMSTestSenders(nil, func(string, string, string) error {
		sends.Add(1)
		return errors.New("modem rejected SMS")
	})
	session := connectMCPTestClient(t, &Server{pool: pool})
	for _, tc := range []struct {
		name, selector, value, expected string
	}{
		{name: "offline", selector: "device_id", value: "offline", expected: `"status_code":404`},
		{name: "changed SIM", selector: "iccid", value: "8944000000000000602", expected: `"status_code":404`},
		{name: "quota", selector: "device_id", value: "dev", expected: "sms_rate_limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "hideck_sms_send", Arguments: map[string]any{"request": map[string]any{tc.selector: tc.value, "phone": "10010", "message": "test"}}})
			if err != nil || !result.IsError {
				t.Fatalf("call=%+v %v", result, err)
			}
			raw, _ := json.Marshal(result.StructuredContent)
			if !strings.Contains(string(raw), tc.expected) || sends.Load() != 0 {
				t.Fatalf("failure=%s sends=%d", raw, sends.Load())
			}
		})
	}
	pool.SetOutboundLimiter(nil)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "hideck_sms_send", Arguments: map[string]any{"request": map[string]any{"device_id": "dev", "phone": "10010", "message": "test"}}})
	if err != nil || !result.IsError || sends.Load() != 1 {
		t.Fatalf("write retried or failure hidden: %+v err=%v sends=%d", result, err, sends.Load())
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), "modem rejected SMS") {
		t.Fatalf("real failure lost: %s", raw)
	}
}

func TestMCPContactFileImportWithRealSDK(t *testing.T) {
	openTestDB(t)
	session := connectMCPTestClient(t, &Server{})
	content := base64.StdEncoding.EncodeToString([]byte("BEGIN:VCARD\nVERSION:3.0\nFN:Support\nTEL:10086\nEND:VCARD\n"))
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "hideck_contact_import", Arguments: map[string]any{"filename": "contacts.VCF", "content_base64": content}})
	if err != nil || result.IsError {
		t.Fatalf("import=%+v %v", result, err)
	}
	contacts, err := db.ListPhoneContacts(t.Context())
	if err != nil || len(contacts) != 1 || contacts[0].Name != "Support" {
		t.Fatalf("contacts=%+v %v", contacts, err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "hideck_contact_import", Arguments: map[string]any{"filename": "/private/contacts.vcf", "content_base64": content}})
	if err != nil || !result.IsError {
		t.Fatalf("host path accepted: %+v %v", result, err)
	}
}

func TestMCPBindingCannotSurviveConcurrentRevocation(t *testing.T) {
	testMCPBindingInvalidation(t, "")
}

func TestMCPBindingCannotSurviveConcurrentDeletion(t *testing.T) {
	testMCPBindingInvalidation(t, "/record")
}

func testMCPBindingInvalidation(t *testing.T, suffix string) {
	t.Helper()
	server := &Server{fullCfg: &config.Config{MCP: config.MCPConfig{Enabled: true}}, mcpTokens: mcpserver.NewTokens(&memoryMCPTokens{})}
	token, _, err := server.mcpTokens.Create(t.Context(), "audio")
	if err != nil {
		t.Fatal(err)
	}
	validating, release := make(chan struct{}, 1), make(chan struct{})
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error {
		validating <- struct{}{}
		<-release
		return nil
	})
	router := gin.New()
	server.registerMCPManagement(router.Group("/api"))
	bound, revoked := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(bound)
		request := httptest.NewRequest(http.MethodPost, "/api/mcp/phone-connections", strings.NewReader(`{"token_id":"`+token.ID+`","name":"audio","media_id":"m","lease":"l"}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), request)
	}()
	<-validating
	go func() {
		defer close(revoked)
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/mcp/tokens/"+token.ID+suffix, nil))
	}()
	// Revocation may wait for the in-flight bind, but must invalidate it before
	// returning. The gate also reproduces a revoke overtaking media validation.
	const schedulingWindow = 25 * time.Millisecond
	select {
	case <-revoked:
	case <-time.After(schedulingWindow):
	}
	close(release)
	<-bound
	<-revoked
	if _, err := server.mcpTokens.Active(t.Context(), token.ID); err == nil {
		t.Fatal("revoked token remains active")
	}
	if entries := server.mcpConnections.List(token.ID); len(entries) != 0 {
		t.Fatalf("binding recreated after revocation: %+v", entries)
	}
}
