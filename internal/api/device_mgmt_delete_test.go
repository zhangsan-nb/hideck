package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/device"
)

func TestDeleteManagedDevicePreservesConfigOnStopFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := writeDeviceMgmtLimitConfig(t, "devices:\n  - id: wwan0\n    device_backend: qmi\n")
	if err := config.InitGlobalManager(path); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{true, false} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Params = gin.Params{{Key: "device_id", Value: "wwan0"}}
		detached := false
		deleteManagedDevice(ctx, path, managedDeviceDeleteOps{
			abandon: func(id string) error {
				if id != "wwan0" {
					t.Fatal("wrong device", id)
				}
				if fail {
					return errors.New("modem hangup failed")
				}
				return nil
			},
			detach: func(string) error {
				detached = true
				return nil
			},
		})
		want := http.StatusOK
		if fail {
			want = http.StatusInternalServerError
		}
		if rec.Code != want {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
		}
		got, _ := config.GetDeviceByID("wwan0")
		if (got != nil) != fail {
			t.Fatalf("config preserved=%v stop failed=%v", got != nil, fail)
		}
		if detached == fail {
			t.Fatalf("binding detached=%v stop failed=%v", detached, fail)
		}
	}
}

func TestHandleDeviceMgmtDeleteDeviceRemovesConfigWhileRecovering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := writeDeviceMgmtLimitConfig(t, `
devices:
  - id: wwan0
    device_backend: qmi
    modem_imei: "860000000001001"
  - id: wwan1
    device_backend: qmi
    modem_imei: "860000000002002"
notification_device_filters:
  telegram: [wwan0, wwan1]
`)
	if err := config.InitGlobalManager(path); err != nil {
		t.Fatalf("InitGlobalManager() error = %v", err)
	}

	pool := device.NewPool(&config.Config{})
	pool.ForceRebuildingForTest("wwan0")

	server := &Server{
		pool: pool, configPath: path,
		fullCfg: &config.Config{NotificationDeviceFilters: map[string][]string{"telegram": {"wwan0", "wwan1"}}},
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "device_id", Value: "wwan0"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/devices/wwan0", nil)

	server.handleDeviceMgmtDeleteDevice(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body=%s want status ok", rec.Body.String())
	}
	if got, _ := config.GetDeviceByID("wwan0"); got != nil {
		t.Fatalf("wwan0 still in config after delete: %+v", got)
	}
	if got, _ := config.GetDeviceByID("wwan1"); got == nil {
		t.Fatal("wwan1 was deleted with wwan0")
	}
	if got := server.fullCfg.NotificationDeviceFilters["telegram"]; len(got) != 1 || got[0] != "wwan1" {
		t.Fatalf("runtime notification filter = %v, want [wwan1]", got)
	}
}

func TestWithoutNotificationDeviceKeepsSoleTargetRestrictive(t *testing.T) {
	filters := withoutNotificationDevice(map[string][]string{
		"bark":     {"wwan0"},
		"telegram": {"wwan0", "wwan1"},
	}, "wwan0")

	if got := filters["bark"]; len(got) != 1 || got[0] != "wwan0" {
		t.Fatalf("bark filter = %v, want inactive restrictive target [wwan0]", got)
	}
	if got := filters["telegram"]; len(got) != 1 || got[0] != "wwan1" {
		t.Fatalf("telegram filter = %v, want [wwan1]", got)
	}
}

func TestHandleDeviceMgmtOverviewLiteReturnsNotFoundAfterDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := writeDeviceMgmtLimitConfig(t, "devices: []\n")
	if err := config.InitGlobalManager(path); err != nil {
		t.Fatalf("InitGlobalManager() error = %v", err)
	}
	server := &Server{pool: device.NewPool(&config.Config{}), configPath: path}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "device_id", Value: "wwan0"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/devices/wwan0/overview", nil)

	server.handleDeviceMgmtOverviewLite(ctx)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestHandleDeviceMgmtDeleteDeviceIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := writeDeviceMgmtLimitConfig(t, `
devices:
  - id: wwan0
    device_backend: qmi
`)
	if err := config.InitGlobalManager(path); err != nil {
		t.Fatalf("InitGlobalManager() error = %v", err)
	}
	server := &Server{pool: device.NewPool(&config.Config{}), configPath: path}

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Params = gin.Params{{Key: "device_id", Value: "wwan0"}}
		ctx.Request = httptest.NewRequest(http.MethodDelete, "/devices/wwan0", nil)
		server.handleDeviceMgmtDeleteDevice(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("round %d status=%d want=%d body=%s", i+1, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}
