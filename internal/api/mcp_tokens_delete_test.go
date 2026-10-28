package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func TestMCPTokenDeleteRequiresWebSessionAndClearsAudio(t *testing.T) {
	server, token, secret := newMCPTokenDeletionServer(t, &memoryMCPTokens{})
	router := gin.New()
	group := router.Group("/api", server.authMiddleware())
	server.registerMCPManagement(group)
	web := testSessionToken(t, "secret", time.Now().Add(time.Hour))
	for _, test := range []struct {
		name, credential, id string
		status               int
	}{
		{name: "missing session", id: token.ID, status: http.StatusUnauthorized},
		{name: "MCP token", credential: secret, id: token.ID, status: http.StatusUnauthorized},
		{name: "missing record", credential: web, id: "missing", status: http.StatusNotFound},
		{name: "delete", credential: web, id: token.ID, status: http.StatusNoContent},
		{name: "already deleted", credential: web, id: token.ID, status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodDelete, "/api/mcp/tokens/"+test.id+"/record", nil)
			request.Header.Set("Authorization", "Bearer "+test.credential)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if _, err := server.mcpTokens.Authenticate(t.Context(), secret); !errors.Is(err, mcpserver.ErrUnauthorized) {
		t.Fatalf("deleted token authenticated: %v", err)
	}
	list, err := server.mcpTokens.List(t.Context())
	if err != nil || len(list) != 0 || len(server.mcpConnections.List(token.ID)) != 0 {
		t.Fatalf("deleted token or audio remained: tokens=%d err=%v", len(list), err)
	}
}

type failingMCPTokenDeletion struct{ mcpserver.TokenStore }

func (failingMCPTokenDeletion) Delete(context.Context, string) error {
	return errors.New("token database unavailable")
}

func TestMCPTokenDeleteFailurePreservesRecordAndAudio(t *testing.T) {
	server, token, secret := newMCPTokenDeletionServer(t, failingMCPTokenDeletion{&memoryMCPTokens{}})
	router := gin.New()
	server.registerMCPManagement(router.Group("/api"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/mcp/tokens/"+token.ID+"/record", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("deletion failure hidden: %d", response.Code)
	}
	if _, err := server.mcpTokens.Authenticate(t.Context(), secret); err != nil {
		t.Fatalf("failed deletion invalidated token: %v", err)
	}
	if len(server.mcpConnections.List(token.ID)) != 1 {
		t.Fatal("failed deletion removed audio authorization")
	}
}

func newMCPTokenDeletionServer(t *testing.T, store mcpserver.TokenStore) (*Server, mcpserver.Token, string) {
	t.Helper()
	server := &Server{auth: config.WebConfig{Username: "admin", Password: "secret"}, mcpTokens: mcpserver.NewTokens(store)}
	token, secret, err := server.mcpTokens.Create(t.Context(), "delete me")
	if err != nil {
		t.Fatal(err)
	}
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	if _, err := server.mcpConnections.Bind(mcpserver.Connection{TokenID: token.ID}, mcpserver.AudioCredentials{Owner: "admin", MediaID: "media", Lease: "lease"}); err != nil {
		t.Fatal(err)
	}
	return server, token, secret
}
