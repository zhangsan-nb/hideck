package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestUpdateSystemInFilePersistsOpenWRTSetting(t *testing.T) {
	path := writeTempConfig(t, `
server:
  port: 7575
devices:
  - id: wwan0
    modem_imei: "123456789012345"
`)
	if err := UpdateSystemInFile(path, SystemConfig{OpenWRTDynamicInterfaces: true}); err != nil {
		t.Fatalf("UpdateSystemInFile() error = %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.System.OpenWRTDynamicInterfaces {
		t.Fatal("OpenWRTDynamicInterfaces = false, want true")
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].ModemIMEI != "123456789012345" {
		t.Fatalf("device config changed: %+v", cfg.Devices)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(raw), "openwrt_dynamic_interfaces: true") {
		t.Fatalf("persisted config =\n%s", raw)
	}
}

func TestSystemSettingDefaultsOff(t *testing.T) {
	path := writeTempConfig(t, "devices: []")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.System.OpenWRTDynamicInterfaces {
		t.Fatal("OpenWRTDynamicInterfaces = true, want default false")
	}
	if cfg.System.AutoAnswer.AudioFile != "data/phone/auto-answer.wav" || len(cfg.System.AutoAnswer.Devices) != 0 {
		t.Fatalf("auto answer defaults = %+v", cfg.System.AutoAnswer)
	}
}

func TestUpdateAutoAnswerInFilePreservesOtherSystemSettings(t *testing.T) {
	path := writeTempConfig(t, "system:\n  openwrt_dynamic_interfaces: true\ndevices: []")
	wantDevice := AutoAnswerDeviceConfig{Enabled: true, DelaySeconds: 45}
	if err := UpdateAutoAnswerDeviceInFile(path, "wwan0", wantDevice); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAutoAnswerAudioFileInFile(path, "data/phone/greeting.wav"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := AutoAnswerConfig{
		AudioFile: "data/phone/greeting.wav", Devices: map[string]AutoAnswerDeviceConfig{"wwan0": wantDevice},
	}
	if !cfg.System.OpenWRTDynamicInterfaces || !reflect.DeepEqual(cfg.System.AutoAnswer, want) {
		t.Fatalf("system config = %+v", cfg.System)
	}
}

func TestDeleteDeviceInFileRemovesItsAutoAnswerSettings(t *testing.T) {
	path := writeTempConfig(t, `
system:
  auto_answer:
    audio_file: greeting.wav
    devices:
      dev1: {enabled: true, delay_seconds: 15}
      dev2: {enabled: true, delay_seconds: 25}
devices:
  - id: dev1
  - id: dev2
`)
	if err := DeleteDeviceInFile(path, "dev1"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.System.AutoAnswer.Devices["dev1"]; exists {
		t.Fatal("deleted device retained its auto-answer settings")
	}
	if got := cfg.System.AutoAnswer.Devices["dev2"]; !got.Enabled || got.DelaySeconds != 25 {
		t.Fatalf("remaining device settings = %+v", got)
	}
}
