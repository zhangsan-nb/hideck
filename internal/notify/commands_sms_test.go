package notify

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost/messaging"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/device"
	"github.com/yibaiba/hideck/pkg/smscodec"
)

type testCommandContext struct {
	replies chan string
}

func (c testCommandContext) Reply(text string)   { c.replies <- text }
func (c testCommandContext) Confirm(string) bool { return true }

func newRoutedSMSTestPool(t *testing.T) (*device.Pool, *device.Worker) {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "commands_sms.db")); err != nil {
		t.Fatalf("db.Init() error=%v", err)
	}
	t.Cleanup(func() { db.DB = nil })
	imei := "860000000000001"
	if err := db.UpsertDevice(imei, "wwan0", "test", ""); err != nil {
		t.Fatalf("UpsertDevice() error=%v", err)
	}
	if err := db.UpsertSIMCard("8944000000000000001", "234150000000001", "", "test", &imei); err != nil {
		t.Fatalf("UpsertSIMCard() error=%v", err)
	}
	pool := device.NewPool(&config.Config{})
	worker := &device.Worker{ID: "wwan0", Config: config.DeviceConfig{
		ID: "wwan0", VoWiFiEnabled: true, PhoneMode: "wifi",
	}}
	pool.AttachWorkerForTest(worker)
	return pool, worker
}

func TestHandleCmdSendSMSFallsBackToCSWithoutDoubleSend(t *testing.T) {
	pool, _ := newRoutedSMSTestPool(t)
	vowifiCalls, csCalls := 0, 0
	pool.SetRoutedSMSTestSenders(
		func(context.Context, string, string, string, smscodec.SubmitOptions) (messaging.SendOutcome, error) {
			vowifiCalls++
			return messaging.SendOutcome{MessageID: "ims-1", SIPCode: 503, RecommendCSFallback: true}, errors.New("503")
		},
		func(string, string, string) error { csCalls++; return nil },
	)
	replies := make(chan string, 1)
	manager := &Manager{pool: pool}
	accepted := manager.handleCmdSendSMS(testCommandContext{replies: replies}, []string{"wwan0", "888", "BAL"})
	if !strings.Contains(accepted, "已受理") || !strings.Contains(accepted, "VoWiFi") {
		t.Fatalf("accepted=%q", accepted)
	}
	select {
	case got := <-replies:
		if !strings.Contains(got, "完成") || !strings.Contains(got, "蜂窝") {
			t.Fatalf("reply=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("command SMS reply was not delivered")
	}
	if vowifiCalls != 1 || csCalls != 1 {
		t.Fatalf("calls vowifi=%d cs=%d", vowifiCalls, csCalls)
	}
}

func TestHandleCmdSendSMSDoesNotFallbackAfterIMSAccept(t *testing.T) {
	pool, _ := newRoutedSMSTestPool(t)
	csCalls := 0
	pool.SetRoutedSMSTestSenders(
		func(context.Context, string, string, string, smscodec.SubmitOptions) (messaging.SendOutcome, error) {
			return messaging.SendOutcome{MessageID: "ims-1", SIPCode: 403, RecommendCSFallback: false}, errors.New("403")
		},
		func(string, string, string) error { csCalls++; return nil },
	)
	replies := make(chan string, 1)
	manager := &Manager{pool: pool}
	_ = manager.handleCmdSendSMS(testCommandContext{replies: replies}, []string{"wwan0", "888", "BAL"})
	select {
	case got := <-replies:
		if !strings.Contains(got, "失败") || !strings.Contains(got, "VoWiFi") {
			t.Fatalf("reply=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("command SMS reply was not delivered")
	}
	if csCalls != 0 {
		t.Fatalf("cs calls=%d", csCalls)
	}
}
