package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func TestMCPDiagnosticsShowRealCallsAndClearRevokedActivity(t *testing.T) {
	server := &Server{auth: config.WebConfig{Username: "admin", Password: "secret"}}
	session := connectMCPTestClient(t, server)
	tokens, err := server.mcpTokens.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id := tokens[0].ID
	before, err := server.inspectMCP(t.Context())
	if err != nil || !before.Ready || before.ToolCount != 125 || before.OutputSchemaCount != 125 || !before.InstructionsAvailable {
		t.Fatalf("diagnostics=%+v err=%v", before, err)
	}
	if before.Tokens[0].Activity == nil || before.Tokens[0].Activity.LastTool != nil {
		t.Fatal("handshake reported as tool call")
	}
	for _, call := range []struct {
		name   string
		failed bool
	}{
		{name: "hideck_phone_audio_connections"}, {name: "hideck_phone_dial", failed: true},
	} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: call.name})
		if err != nil || result.IsError != call.failed {
			t.Fatalf("call=%+v err=%v", result, err)
		}
		activity := server.mcpTokens.Activity(id)
		if activity.LastTool == nil || activity.LastTool.Name != call.name || activity.LastTool.Failed != call.failed || activity.LastTool.At.IsZero() {
			t.Fatalf("activity=%+v", activity)
		}
		activity.LastTool.Name = "caller mutation"
		if server.mcpTokens.Activity(id).LastTool.Name != call.name {
			t.Fatal("activity snapshot is mutable shared state")
		}
	}
	if err := server.mcpTokens.Revoke(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	after, err := server.inspectMCP(t.Context())
	if err != nil || after.Ready || after.Status != "token_required" || after.Tokens[0].Activity != nil {
		t.Fatalf("revoked=%+v err=%v", after, err)
	}
}

func TestMCPDiagnosticsRequiresWebSessionAndOmitsSecrets(t *testing.T) {
	server, token, secret := newMCPTokenDeletionServer(t, &memoryMCPTokens{})
	server.fullCfg = &config.Config{}
	server.disclaimerAcceptances = &disclaimerStoreStub{accepted: true}
	router := gin.New()
	server.registerMCPManagement(router.Group("/api", server.authMiddleware()))
	web := testSessionToken(t, "secret", time.Now().Add(time.Hour))
	for _, credential := range []string{"", secret, web} {
		request := httptest.NewRequest(http.MethodGet, "/api/mcp/diagnostics", nil)
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if credential != web {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", response.Code)
			}
			continue
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response=%d %s", response.Code, response.Body.String())
		}
		for _, sensitive := range []string{secret, token.Digest, "lease", "media-1", "content_base64"} {
			if strings.Contains(response.Body.String(), sensitive) {
				t.Fatalf("diagnostics expose %s", sensitive)
			}
		}
		var data mcpDiagnostics
		if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if data.Ready || data.Status != "disabled" || data.Tokens[0].Activity != nil {
			t.Fatalf("false connection claim: %+v", data)
		}
	}
}

type failingMCPDiagnosticStore struct{ mcpserver.TokenStore }

func (failingMCPDiagnosticStore) List(context.Context) ([]mcpserver.Token, error) {
	return nil, errors.New("database unavailable")
}

func TestMCPDiagnosticsExposeInitializationAndStorageFailures(t *testing.T) {
	server := &Server{mcpTokens: mcpserver.NewTokens(failingMCPDiagnosticStore{&memoryMCPTokens{}})}
	router := gin.New()
	server.registerMCPManagement(router.Group("/api"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/mcp/diagnostics", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "database unavailable") {
		t.Fatalf("storage failure hidden: %d %s", response.Code, response.Body.String())
	}
	server.mcpTokens = mcpserver.NewTokens(&memoryMCPTokens{})
	server.disclaimerAcceptances = &disclaimerStoreStub{accepted: true}
	server.mcpOnce.Do(func() { server.mcpError = errors.New("invalid tool output schema") })
	data, err := server.inspectMCP(t.Context())
	if err != nil || data.Ready || data.Initialized || data.Status != "initialization_failed" || !strings.Contains(data.Message, "invalid tool output schema") {
		t.Fatalf("initialization failure hidden: %+v %v", data, err)
	}
}
