package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteDeviceInFileRemovesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `devices:
- id: wwan0
  device_backend: qmi
  modem_imei: "860000000001001"
- id: wwan1
  device_backend: qmi
notification_device_filters:
  telegram: [wwan0, wwan1]
  bark: [wwan0]
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := InitGlobalManager(path); err != nil {
		t.Fatalf("InitGlobalManager() error = %v", err)
	}

	if err := DeleteDeviceInFile(path, "wwan0"); err != nil {
		t.Fatalf("DeleteDeviceInFile() error = %v", err)
	}
	if got, _ := GetDeviceByID("wwan0"); got != nil {
		t.Fatalf("wwan0 still loaded after delete: %+v", got)
	}
	if got, _ := GetDeviceByID("wwan1"); got == nil {
		t.Fatal("wwan1 was removed with wwan0")
	}
	filters := GetConfig().NotificationDeviceFilters
	if got := filters["telegram"]; len(got) != 1 || got[0] != "wwan1" {
		t.Fatalf("telegram filter = %v, want [wwan1]", got)
	}
	if got := filters["bark"]; len(got) != 1 || got[0] != "wwan0" {
		t.Fatalf("bark filter = %v, want restrictive tombstone [wwan0]", got)
	}
	if err := DeleteDeviceInFile(path, "wwan0"); err != nil {
		t.Fatalf("second DeleteDeviceInFile() error = %v", err)
	}
}

func TestUpdateDeviceIMEIInFileWritesOnlyIMEI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "devices:\n- id: dev1\n  device_backend: qmi\n  control_device: /dev/cdc-wdm1\n  interface: wwan0\n  at_port: /dev/ttyUSB2\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := UpdateDeviceIMEIInFile(path, map[string]string{"dev1": "860000000008008"}); err != nil {
		t.Fatalf("UpdateDeviceIMEIInFile() error = %v", err)
	}

	updated, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := updated.Devices[0]
	if got.ModemIMEI != "860000000008008" {
		t.Fatalf("ModemIMEI = %q, want 860000000008008", got.ModemIMEI)
	}
	// 零路径架构: Load() 绝不从文件回填运行时路径字段(mapstructure:"-")。
	if got.ControlDevice != "" || got.Interface != "" || got.ATPort != "" {
		t.Fatalf("runtime path fields must not be loaded from file, got: %+v", got)
	}
}

func TestUpdateDeviceIMEIInFileSkipsEmptyIMEI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "devices:\n- id: dev1\n  device_backend: qmi\n  modem_imei: \"123456789012345\"\n  control_device: /dev/cdc-wdm1\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := UpdateDeviceIMEIInFile(path, map[string]string{"dev1": "  "}); err != nil {
		t.Fatalf("UpdateDeviceIMEIInFile() error = %v", err)
	}

	afterCfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() after error = %v", err)
	}
	if len(afterCfg.Devices) != 1 || afterCfg.Devices[0].ModemIMEI != "123456789012345" {
		t.Fatalf("ModemIMEI was changed or erased: %+v", afterCfg.Devices)
	}
}
