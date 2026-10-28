package db

import (
	"context"
	"errors"
	"time"

	"github.com/yibaiba/hideck/internal/mcpserver"
	"gorm.io/gorm"
)

type MCPTokenStore struct{ database *gorm.DB }

func NewMCPTokenStore(database *gorm.DB) *MCPTokenStore { return &MCPTokenStore{database: database} }
func (s *MCPTokenStore) Create(ctx context.Context, token mcpserver.Token) error {
	return s.database.WithContext(ctx).Create(&token).Error
}
func (s *MCPTokenStore) List(ctx context.Context) ([]mcpserver.Token, error) {
	tokens := []mcpserver.Token{}
	err := s.database.WithContext(ctx).Order("created_at DESC").Find(&tokens).Error
	return tokens, err
}
func (s *MCPTokenStore) FindDigest(ctx context.Context, digest string) (mcpserver.Token, error) {
	return s.find(ctx, "digest = ?", digest)
}
func (s *MCPTokenStore) FindID(ctx context.Context, id string) (mcpserver.Token, error) {
	return s.find(ctx, "id = ?", id)
}
func (s *MCPTokenStore) find(ctx context.Context, query string, value string) (mcpserver.Token, error) {
	var token mcpserver.Token
	err := s.database.WithContext(ctx).Where(query, value).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return token, mcpserver.ErrUnauthorized
	}
	return token, err
}
func (s *MCPTokenStore) Revoke(ctx context.Context, id string, at time.Time) error {
	result := s.database.WithContext(ctx).Model(&mcpserver.Token{}).Where("id = ? AND revoked_at IS NULL", id).Update("revoked_at", at)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return mcpserver.ErrUnauthorized
	}
	return nil
}
func (s *MCPTokenStore) Delete(ctx context.Context, id string) error {
	result := s.database.WithContext(ctx).Where("id = ?", id).Delete(&mcpserver.Token{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return mcpserver.ErrUnauthorized
	}
	return nil
}
