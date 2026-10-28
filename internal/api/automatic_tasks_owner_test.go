package api

import (
	"testing"

	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/device"
)

func TestAutomaticTaskOwnerAllowsSameModemReenumeration(t *testing.T) {
	original := &device.Worker{ID: "wwan0", Config: config.DeviceConfig{ModemIMEI: "860000000000001"}}
	pool := device.NewPool(&config.Config{})
	owner := captureAutomaticTaskDeviceOwner(pool, original)
	replacement := &device.Worker{ID: original.ID, Config: config.DeviceConfig{ModemIMEI: original.Config.ModemIMEI}}
	pool.AttachWorkerForTest(replacement)

	resolved, err := owner.resolve(pool, original.ID, "")
	if err != nil || resolved != replacement {
		t.Fatalf("resolve() worker=%p err=%v", resolved, err)
	}
}

func TestAutomaticTaskOwnerRejectsDifferentModem(t *testing.T) {
	original := &device.Worker{ID: "wwan0", Config: config.DeviceConfig{ModemIMEI: "860000000000001"}}
	pool := device.NewPool(&config.Config{})
	owner := captureAutomaticTaskDeviceOwner(pool, original)
	replacement := &device.Worker{ID: original.ID, Config: config.DeviceConfig{ModemIMEI: "860000000000002"}}
	pool.AttachWorkerForTest(replacement)

	if _, err := owner.resolve(pool, original.ID, ""); err == nil {
		t.Fatal("different physical modem inherited the automatic task")
	}
}

func TestAutomaticTaskOwnerRejectsSameSIMAfterIdentityGenerationChanges(t *testing.T) {
	pool := device.NewPool(&config.Config{})
	worker := &device.Worker{ID: "wwan0", Config: config.DeviceConfig{ModemIMEI: "860000000000001"}}
	worker.BeginSIMIdentityTransition("8944100000000000001", "test_initial")
	pool.AttachWorkerForTest(worker)
	owner := captureAutomaticTaskDeviceOwner(pool, worker)
	worker.BeginSIMIdentityTransition("8944100000000000001", "test_aba")

	if _, err := owner.resolve(pool, worker.ID, ""); err == nil {
		t.Fatal("same ICCID with a new identity generation inherited the automatic task")
	}
}

func TestAutomaticTaskOwnerRejectsSameSIMAfterRemovalEpochChanges(t *testing.T) {
	pool := device.NewPool(&config.Config{})
	original := &device.Worker{ID: "wwan0", Config: config.DeviceConfig{ModemIMEI: "860000000000001"}}
	pool.AttachWorkerForTest(original)
	owner := captureAutomaticTaskDeviceOwner(pool, original)
	if err := pool.RemoveWorker(original.ID); err != nil {
		t.Fatal(err)
	}
	replacement := &device.Worker{ID: original.ID, Config: original.Config}
	pool.AttachWorkerForTest(replacement)

	if _, err := owner.resolve(pool, original.ID, ""); err == nil {
		t.Fatal("same SIM after a removal epoch inherited the automatic task")
	}
}
