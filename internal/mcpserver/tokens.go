package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrUnauthorized = errors.New("无效或已撤销的 MCP 令牌")

type Token struct {
	ID        string     `json:"id" gorm:"primaryKey"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Digest    string     `json:"-" gorm:"uniqueIndex;not null"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

func (Token) TableName() string { return "mcp_tokens" }

type TokenStore interface {
	Create(context.Context, Token) error
	List(context.Context) ([]Token, error)
	FindDigest(context.Context, string) (Token, error)
	FindID(context.Context, string) (Token, error)
	Revoke(context.Context, string, time.Time) error
	Delete(context.Context, string) error
}

type Tokens struct {
	store    TokenStore
	mu       sync.Mutex
	activity map[string]Activity
}

func NewTokens(store TokenStore) *Tokens {
	return &Tokens{store: store, activity: make(map[string]Activity)}
}

func (s *Tokens) Create(ctx context.Context, name string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Token{}, "", errors.New("请输入令牌名称")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return Token{}, "", err
	}
	secret := "hideck_mcp_" + base64.RawURLEncoding.EncodeToString(random)
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Token{}, "", err
	}
	token := Token{ID: hex.EncodeToString(id), Name: name, Prefix: secret[:18], Digest: digest(secret), CreatedAt: time.Now().UTC()}
	if err := s.store.Create(ctx, token); err != nil {
		return Token{}, "", err
	}
	return token, secret, nil
}

func (s *Tokens) Authenticate(ctx context.Context, secret string) (Token, error) {
	if !strings.HasPrefix(secret, "hideck_mcp_") {
		return Token{}, ErrUnauthorized
	}
	// Serialize observation with revocation/deletion; in-flight completions cannot restore it.
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.store.FindDigest(ctx, digest(secret))
	if err != nil {
		return Token{}, err
	}
	if token.ID == "" || token.RevokedAt != nil {
		return Token{}, ErrUnauthorized
	}
	activity := s.activity[token.ID]
	activity.LastRequestAt = time.Now().UTC()
	s.activity[token.ID] = activity
	return token, nil
}

func (s *Tokens) Active(ctx context.Context, id string) (Token, error) {
	token, err := s.store.FindID(ctx, id)
	if err != nil {
		return Token{}, err
	}
	if token.ID == "" || token.RevokedAt != nil {
		return Token{}, ErrUnauthorized
	}
	return token, nil
}

func (s *Tokens) List(ctx context.Context) ([]Token, error) { return s.store.List(ctx) }
func (s *Tokens) Revoke(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Revoke(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	delete(s.activity, id)
	return nil
}
func (s *Tokens) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	delete(s.activity, id)
	return nil
}
func digest(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(hash[:])
}
