package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/device"
)

func TestMCPAndRESTShareESIMMutationLock(t *testing.T) {
	pool := device.NewPool(&config.Config{})
	server := &Server{pool: pool}
	session := connectMCPTestClient(t, server)
	router := gin.New()
	router.Use(server.mbnMutationMiddleware())
	router.PATCH("/api/devices/:device_id/esim/profiles/:iccid", server.handleEsimRenameProfile)
	release := pool.AcquireManagedDeviceMutation("dev")
	restDone, mcpDone := make(chan int, 1), make(chan *mcp.CallToolResult, 1)
	errors := make(chan error, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPatch, "/api/devices/dev/esim/profiles/card", strings.NewReader(`{"name":"new name"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		restDone <- response.Code
	}()
	go func() {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "hideck_esim_rename", Arguments: map[string]any{"device_id": "dev", "iccid": "card", "request": map[string]any{"name": "new name"}}})
		if err != nil {
			errors <- err
			return
		}
		mcpDone <- result
	}()
	const schedulingWindow = 25 * time.Millisecond
	select {
	case <-restDone:
		release()
		t.Fatal("REST bypassed the current eSIM mutation owner")
	case <-mcpDone:
		release()
		t.Fatal("MCP bypassed the current eSIM mutation owner")
	case err := <-errors:
		release()
		t.Fatal(err)
	case <-time.After(schedulingWindow):
	}
	release()
	if status := <-restDone; status != http.StatusNotFound {
		t.Fatalf("REST after release=%d", status)
	}
	select {
	case result := <-mcpDone:
		raw, _ := json.Marshal(result.StructuredContent)
		if !result.IsError || !strings.Contains(string(raw), `"status_code":404`) {
			t.Fatalf("MCP after release=%s", raw)
		}
	case err := <-errors:
		t.Fatal(err)
	}
}
