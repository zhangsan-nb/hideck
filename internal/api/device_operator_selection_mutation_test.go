package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/device"
)

func TestSetOperatorSelectionWaitsForManagedDeviceMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := device.NewPool(&config.Config{})
	t.Cleanup(func() { _ = pool.Shutdown() })
	releaseMutation := pool.AcquireManagedDeviceMutation("dev-1")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "device_id", Value: "dev-1"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/devices/dev-1/operator-selection",
		strings.NewReader(`{"mode":"automatic"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Server{pool: pool}).handleDeviceMgmtSetOperatorSelection(ctx)
	}()

	select {
	case <-done:
		t.Fatal("operator selection bypassed the managed device mutation lease")
	case <-time.After(20 * time.Millisecond):
	}
	releaseMutation()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operator selection did not resume after mutation lease release")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDeviceActionsWaitForManagedDeviceMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		method string
		body   string
		params gin.Params
		handle func(*Server, *gin.Context)
	}{
		{name: "refresh", method: http.MethodPost, handle: (*Server).handleDeviceMgmtRefreshInfo},
		{name: "flight mode", method: http.MethodPut, body: `{"enabled":true}`, handle: (*Server).handleDeviceMgmtSetFlightMode},
		{name: "reboot", method: http.MethodPost, handle: (*Server).handleDeviceMgmtReboot},
		{name: "start network", method: http.MethodPost, handle: (*Server).handleDeviceMgmtStartNetwork},
		{name: "stop network", method: http.MethodPost, handle: (*Server).handleDeviceMgmtStopNetwork},
		{name: "esim switch", method: http.MethodPost, body: `{"iccid":"8901"}`, handle: (*Server).handleEsimSwitchProfile},
		{name: "esim disable", method: http.MethodPost, body: `{"iccid":"8901"}`, handle: (*Server).handleEsimDisableProfile},
		{name: "esim download", method: http.MethodGet, handle: (*Server).handleEsimDownloadProfile},
		{name: "esim delete", method: http.MethodDelete, handle: (*Server).handleEsimDeleteProfile},
		{
			name: "esim notification retry", method: http.MethodPost,
			params: gin.Params{{Key: "sequence", Value: "1"}}, handle: (*Server).handleEsimRetryNotification,
		},
		{
			name: "esim rename", method: http.MethodPatch, body: `{"name":"primary"}`,
			params: gin.Params{{Key: "iccid", Value: "8901"}}, handle: (*Server).handleEsimRenameProfile,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := device.NewPool(&config.Config{})
			t.Cleanup(func() { _ = pool.Shutdown() })
			releaseMutation := pool.AcquireManagedDeviceMutation("dev-1")
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = append(gin.Params{{Key: "device_id", Value: "dev-1"}}, test.params...)
			ctx.Request = httptest.NewRequest(test.method, "/devices/dev-1/action", strings.NewReader(test.body))
			if test.body != "" {
				ctx.Request.Header.Set("Content-Type", "application/json")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				test.handle(&Server{pool: pool}, ctx)
			}()

			select {
			case <-done:
				t.Fatal("device action bypassed the managed mutation lease")
			case <-time.After(20 * time.Millisecond):
			}
			releaseMutation()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("device action did not resume after mutation lease release")
			}
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPolicyActionsWaitForManagedDeviceMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		body   string
		handle func(*Server, *gin.Context)
	}{
		{name: "network policy", body: `{"enabled":true}`, handle: (*Server).handleDeviceNetworkPatch},
		{name: "phone policy", body: `{"enabled":true}`, handle: (*Server).handleDeviceVoWiFiPatch},
		{name: "vowifi reconnect", handle: (*Server).handleDeviceMgmtReconnectVoWiFi},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := device.NewPool(&config.Config{})
			t.Cleanup(func() { _ = pool.Shutdown() })
			releaseMutation := pool.AcquireManagedDeviceMutation("dev-1")
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "device_id", Value: "dev-1"}}
			ctx.Request = httptest.NewRequest(http.MethodPatch, "/devices/dev-1/action", strings.NewReader(test.body))
			if test.body != "" {
				ctx.Request.Header.Set("Content-Type", "application/json")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				test.handle(&Server{pool: pool}, ctx)
			}()

			select {
			case <-done:
				t.Fatal("policy action bypassed the managed mutation lease")
			case <-time.After(20 * time.Millisecond):
			}
			releaseMutation()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("policy action did not resume after mutation lease release")
			}
		})
	}
}

func TestRefreshManagedWorkerRuntimeRejectsReplacement(t *testing.T) {
	pool := device.NewPool(&config.Config{})
	t.Cleanup(func() { _ = pool.Shutdown() })
	oldWorker := &device.Worker{ID: "dev-1"}
	replacement := &device.Worker{ID: oldWorker.ID}
	setNestedPrivateField(t, pool, []string{"workers"}, map[string]*device.Worker{replacement.ID: replacement})
	if (&Server{pool: pool}).refreshManagedWorkerRuntime(oldWorker, "stale_refresh") {
		t.Fatal("stale worker refresh was accepted after replacement")
	}
}
