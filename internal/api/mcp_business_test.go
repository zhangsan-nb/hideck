package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func TestMCPAndRESTShareContactWrites(t *testing.T) {
	if err := db.Init(filepath.Join(t.TempDir(), "contacts.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB = nil })
	server := &Server{auth: config.WebConfig{Username: "web", Password: "secret"}}
	result, err := server.executeMCPBusiness(context.Background(), mcpOperation{name: "hideck_contact_save", run: server.usePhoneContactsUpsert}, mcpArguments{raw: json.RawMessage(`{"request":{"number":"10086","name":"移动客服"}}`)})
	if err != nil || result.Failed {
		t.Fatalf("save=%+v err=%v", result, err)
	}
	recorder := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(recorder)
	requestContext.Request = httptest.NewRequest(http.MethodGet, "/api/phone/contacts", nil)
	server.handlePhoneContactsList(requestContext)
	if !strings.Contains(recorder.Body.String(), "移动客服") {
		t.Fatalf("REST missed MCP write: %s", recorder.Body.String())
	}
	result, err = server.executeMCPBusiness(requestContext.Request.Context(), mcpOperation{name: "hideck_contact_delete", run: server.usePhoneContactsDelete}, mcpArguments{raw: json.RawMessage(`{"number":"10086"}`)})
	if err != nil || result.Failed {
		t.Fatalf("delete=%+v err=%v", result, err)
	}
	rows, err := db.ListPhoneContacts(requestContext.Request.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("delete not committed: %+v %v", rows, err)
	}
}
func TestBusinessProgressReportsRealFailureAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	call := &businessCall{ctx: ctx, cancel: cancel, progress: func(message string, pct float64) error {
		count++
		if message != "安装" || pct != 80 {
			t.Fatalf("progress=%q %v", message, pct)
		}
		return context.Canceled
	}}
	businessProgress(call, `{"step":"install","msg":"安装","pct":80}`)
	if count != 1 || ctx.Err() == nil || call.progressErr == nil {
		t.Fatal("progress failure was swallowed")
	}
	businessProgress(call, `{"step":"error","msg":"真实失败","code":"esim_error"}`)
	result, err := mcpBusinessResult(call)
	if err != nil || !result.Failed {
		t.Fatalf("failure=%+v %v", result, err)
	}
}
func TestMCPSettingsPersistenceAndWebOnlyTokenManagement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeMCPConfig(t, path)
	server := &Server{configPath: path, fullCfg: &config.Config{}, auth: config.WebConfig{Username: "admin", Password: "secret"}, mcpTokens: mcpserver.NewTokens(&memoryMCPTokens{})}
	server.mcpConnections = mcpserver.NewConnections(func(mcpserver.AudioCredentials) error { return nil })
	router := gin.New()
	api := router.Group("/api")
	api.Use(server.authMiddleware())
	server.registerMCPManagement(api)
	webToken := testSessionToken(t, "secret", time.Now().Add(time.Hour))
	request := httptest.NewRequest(http.MethodPut, "/api/settings/mcp", strings.NewReader(`{"enabled":true}`))
	request.Header.Set("Authorization", "Bearer "+webToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !server.mcpEnabled() {
		t.Fatalf("config=%d %s", response.Code, response.Body.String())
	}
	_, secret, err := server.mcpTokens.Create(context.Background(), "IDE")
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/mcp/tokens", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("MCP token accessed web: %d", response.Code)
	}
	data := readMCPConfig(t, path)
	if !strings.Contains(data, "enabled: true") {
		t.Fatalf("not persisted: %s", data)
	}
}

func writeMCPConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("server: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
func readMCPConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
