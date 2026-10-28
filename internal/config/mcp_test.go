package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPConfigDefaultsOffAndPersistsWithoutOtherChanges(t *testing.T) {
	if (Config{}).MCP.Enabled {
		t.Fatal("MCP must default off")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 7575\nweb:\n  username: admin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMCPInFile(path, MCPConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || !loaded.MCP.Enabled {
		t.Fatalf("enabled state not restored from config: %+v %v", loaded, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "enabled: true") || !strings.Contains(string(data), "username: admin") || !strings.Contains(string(data), "port: 7575") {
		t.Fatalf("config changed unexpectedly: %s", data)
	}
	if err := UpdateMCPInFile(path, MCPConfig{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "enabled: false") {
		t.Fatalf("disable: %s %v", data, err)
	}
}
