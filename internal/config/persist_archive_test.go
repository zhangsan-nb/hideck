package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateArchiveInFilePreservesOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 7575\nsystem:\n  openwrt_dynamic_interfaces: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := ArchiveConfig{
		Enabled: true, Endpoint: "https://dav.example/remote.php/dav/files/test", Username: "alice",
		Password: "secret", BasePath: "hideck/lab", IntervalHours: 6, RetentionDays: 45,
		AutoCleanup: true, IncludeSMS: true, IncludeCalls: true, IncludeRecordings: true,
	}
	if err := UpdateArchiveInFile(path, want); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Archive != want {
		t.Fatalf("archive = %+v, want %+v", loaded.Archive, want)
	}
	if !loaded.System.OpenWRTDynamicInterfaces {
		t.Fatalf("unrelated system settings changed: %+v", loaded.System)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "port: 7575") {
		t.Fatalf("unrelated server setting missing:\n%s", raw)
	}
}
