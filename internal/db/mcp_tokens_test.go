package db

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/yibaiba/hideck/internal/mcpserver"
	"gorm.io/gorm"
)

func TestMCPTokensPersistDigestOnlyAndRevoke(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mcp.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&mcpserver.Token{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first := mcpserver.NewTokens(NewMCPTokenStore(database))
	token, secret, err := first.Create(ctx, "  IDE  ")
	if err != nil {
		t.Fatal(err)
	}
	var stored mcpserver.Token
	if err := database.First(&stored, "id = ?", token.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Name != "IDE" || stored.Digest == secret || len(stored.Digest) != 64 || !strings.HasPrefix(secret, stored.Prefix) {
		t.Fatal("incorrect persisted token metadata")
	}
	serialized, _ := json.Marshal(stored)
	if strings.Contains(string(serialized), stored.Digest) || strings.Contains(string(serialized), secret) {
		t.Fatal("token secret/digest leaked in list JSON")
	}
	second := mcpserver.NewTokens(NewMCPTokenStore(database))
	if got, err := second.Authenticate(ctx, secret); err != nil || got.ID != token.ID {
		t.Fatalf("token lost after reopen: %+v %v", got, err)
	}
	if err := second.Revoke(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Authenticate(ctx, secret); !errors.Is(err, mcpserver.ErrUnauthorized) {
		t.Fatalf("revoked token accepted: %v", err)
	}
	if _, err := first.Active(ctx, token.ID); !errors.Is(err, mcpserver.ErrUnauthorized) {
		t.Fatalf("revoked token by id: %v", err)
	}
}

func TestMCPTokensDeleteActiveAndRevoked(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "revoked"}[revoked], func(t *testing.T) {
			database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mcp.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.AutoMigrate(&mcpserver.Token{}); err != nil {
				t.Fatal(err)
			}
			tokens := mcpserver.NewTokens(NewMCPTokenStore(database))
			ctx := t.Context()
			token, secret, err := tokens.Create(ctx, "delete me")
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := tokens.Create(ctx, "keep me")
			if err != nil {
				t.Fatal(err)
			}
			if revoked {
				if err := tokens.Revoke(ctx, token.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := tokens.Delete(ctx, token.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := tokens.Authenticate(ctx, secret); !errors.Is(err, mcpserver.ErrUnauthorized) {
				t.Fatalf("deleted token still authenticates: %v", err)
			}
			for _, id := range []string{token.ID, "' OR 1=1 --"} {
				if err := tokens.Delete(ctx, id); !errors.Is(err, mcpserver.ErrUnauthorized) {
					t.Fatalf("missing token deletion: %v", err)
				}
			}
			list, err := tokens.List(ctx)
			if err != nil || len(list) != 1 || list[0].ID != other.ID {
				t.Fatalf("incorrect remaining tokens: count=%d err=%v", len(list), err)
			}
		})
	}
}
