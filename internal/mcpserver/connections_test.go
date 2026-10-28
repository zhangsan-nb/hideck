package mcpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestConnectionsEnforceTokenOwnerAndMediaLifecycle(t *testing.T) {
	valid := true
	registry := NewConnections(func(c AudioCredentials) error {
		if !valid || c.Owner != "web" || c.Lease != "lease" {
			return errors.New("invalid lease")
		}
		return nil
	})
	credentials := AudioCredentials{Owner: "web", MediaID: "media-1", Lease: "lease"}
	bound, err := registry.Bind(Connection{TokenID: "token-1", Name: "tab-1"}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("token-2", bound.ID); err == nil {
		t.Fatal("other token gained audio")
	}
	listed := registry.List("token-1")
	raw, _ := json.Marshal(listed)
	if len(listed) != 1 || strings.Contains(string(raw), "lease") || strings.Contains(string(raw), "media-1") || strings.Contains(string(raw), "token-1") {
		t.Fatalf("credentials exposed: %s", raw)
	}
	valid = false
	if _, err := registry.Resolve("token-1", bound.ID); err == nil {
		t.Fatal("disconnected media accepted")
	}
	if len(registry.List("token-1")) != 0 {
		t.Fatal("disconnected media listed")
	}
	valid = true
	credentials.MediaID = "media-2"
	updated, err := registry.Bind(Connection{ID: bound.ID, TokenID: "token-1", Name: "tab-1"}, credentials)
	if err != nil || updated.ID != bound.ID {
		t.Fatalf("rebuild: %+v %v", updated, err)
	}
	resolved, err := registry.Resolve("token-1", bound.ID)
	if err != nil || resolved.MediaID != "media-2" {
		t.Fatalf("stale media: %+v %v", resolved, err)
	}
	if err := registry.Delete(bound.ID, "another web user"); err == nil {
		t.Fatal("other owner removed connection")
	}
	registry.Revoke("token-1")
	if _, err := registry.Resolve("token-1", bound.ID); err == nil {
		t.Fatal("revoked binding survived")
	}
}
func TestConnectionsMultipleTabsAndConcurrentRevoke(t *testing.T) {
	registry := NewConnections(func(AudioCredentials) error { return nil })
	first, err := registry.Bind(Connection{TokenID: "token", Name: "one"}, AudioCredentials{Owner: "web", MediaID: "one", Lease: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Bind(Connection{TokenID: "token", Name: "two"}, AudioCredentials{Owner: "web", MediaID: "two", Lease: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(registry.List("token")) != 2 {
		t.Fatal("multiple tabs conflated")
	}
	var group sync.WaitGroup
	for range 10 {
		group.Go(func() { registry.List("token"); _, _ = registry.Resolve("token", first.ID); registry.Revoke("token") })
	}
	group.Wait()
	if len(registry.List("token")) != 0 {
		t.Fatal("revoke not applied")
	}
}
