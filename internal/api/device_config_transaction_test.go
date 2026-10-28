package api

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yibaiba/hideck/internal/config"
)

func TestConcurrentDeviceAddsCannotClaimSameIMEI(t *testing.T) {
	path := writeDeviceTransactionConfig(t, "devices: []\n")
	candidates := []config.DeviceConfig{
		{ID: "a", ModemIMEI: "860000000001001", DeviceBackend: "qmi"},
		{ID: "b", ModemIMEI: "860000000001001", DeviceBackend: "qmi"},
	}
	errs := runConcurrentDeviceWrites(candidates, func(candidate config.DeviceConfig) error {
		return config.AddDeviceInFileValidated(
			path, candidate, managedDeviceListValidator(candidate, "", true),
		)
	})
	assertSingleBindingWinner(t, path, "860000000001001", errs)
}

func TestConcurrentDeviceUpdatesCannotClaimSameIMEI(t *testing.T) {
	path := writeDeviceTransactionConfig(t, "devices:\n- id: a\n  modem_imei: '860000000001001'\n  device_backend: qmi\n- id: b\n  modem_imei: '860000000002002'\n  device_backend: qmi\n")
	candidates := []config.DeviceConfig{
		{ID: "a", ModemIMEI: "860000000009009", DeviceBackend: "qmi"},
		{ID: "b", ModemIMEI: "860000000009009", DeviceBackend: "qmi"},
	}
	errs := runConcurrentDeviceWrites(candidates, func(candidate config.DeviceConfig) error {
		return config.UpdateDeviceInFileValidated(path, config.DeviceUpdateRequest{
			DeviceID: candidate.ID,
			Device:   candidate,
			Validate: managedDeviceListValidator(candidate, candidate.ID, false),
		})
	})
	assertSingleBindingWinner(t, path, "860000000009009", errs)
}

func writeDeviceTransactionConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runConcurrentDeviceWrites(
	candidates []config.DeviceConfig,
	write func(config.DeviceConfig) error,
) []error {
	start := make(chan struct{})
	errs := make([]error, len(candidates))
	var group sync.WaitGroup
	for index, candidate := range candidates {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errs[index] = write(candidate)
		}()
	}
	close(start)
	group.Wait()
	return errs
}

func assertSingleBindingWinner(t *testing.T, path, imei string, errs []error) {
	t.Helper()
	succeeded, conflicted := 0, 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var conflict *deviceBindingConflict
		if errors.As(err, &conflict) {
			conflicted++
			continue
		}
		t.Fatalf("unexpected write error: %v", err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("writes succeeded=%d conflicted=%d, want 1/1", succeeded, conflicted)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	for _, device := range loaded.Devices {
		if config.NormalizeIMEI(device.ModemIMEI) == config.NormalizeIMEI(imei) {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("persisted duplicate claims: %+v", loaded.Devices)
	}
}
