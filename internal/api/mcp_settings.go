package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

func (s *Server) mcpEnabled() bool {
	s.mcpMu.RLock()
	defer s.mcpMu.RUnlock()
	return s.fullCfg != nil && s.fullCfg.MCP.Enabled
}
func (s *Server) registerMCPManagement(api *gin.RouterGroup) {
	api.GET("/settings/mcp", s.handleMCPSettingsGet)
	api.PUT("/settings/mcp", s.handleMCPSettingsPut)
	api.GET("/mcp/diagnostics", s.handleMCPDiagnostics)
	api.GET("/mcp/tokens", s.handleMCPTokensList)
	api.POST("/mcp/tokens", s.handleMCPTokensCreate)
	api.DELETE("/mcp/tokens/:id", s.handleMCPTokensRevoke)
	api.DELETE("/mcp/tokens/:id/record", s.handleMCPTokensDelete)
	api.POST("/mcp/phone-connections", s.handleMCPConnectionBind)
	api.PUT("/mcp/phone-connections/:id", s.handleMCPConnectionBind)
	api.DELETE("/mcp/phone-connections/:id", s.handleMCPConnectionDelete)
}
func (s *Server) handleMCPSettingsGet(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"enabled": s.mcpEnabled(), "path": "/mcp"})
}
func (s *Server) handleMCPSettingsPut(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled 为必填布尔值"})
		return
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if s.fullCfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "配置未初始化"})
		return
	}
	next := config.MCPConfig{Enabled: *req.Enabled}
	if err := config.UpdateMCPInFile(s.configPath, next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Notification settings copy the complete config under this existing lock.
	s.notificationConfigMu.Lock()
	s.fullCfg.MCP = next
	s.notificationConfigMu.Unlock()
	if !next.Enabled && s.mcpConnections != nil {
		s.mcpConnections.Clear()
	}
	c.JSON(http.StatusOK, next)
}
func (s *Server) requireMCPManagement(c *gin.Context) bool {
	if s.mcpTokens != nil {
		return true
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP 令牌存储未初始化"})
	return false
}
func (s *Server) handleMCPTokensList(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}
	tokens, err := s.mcpTokens.List(c.Request.Context())
	if err != nil {
		mcpManagementError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"tokens": tokens})
}
func (s *Server) handleMCPTokensCreate(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入令牌名称"})
		return
	}
	token, secret, err := s.mcpTokens.Create(c.Request.Context(), req.Name)
	if err != nil {
		mcpManagementError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"token": token, "secret": secret})
}
func (s *Server) handleMCPTokensRevoke(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if err := s.mcpTokens.Revoke(c.Request.Context(), c.Param("id")); err != nil {
		mcpManagementError(c, err)
		return
	}
	s.mcpConnections.Revoke(c.Param("id"))
	c.Status(http.StatusNoContent)
}
func (s *Server) handleMCPTokensDelete(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}

	// Serialize deletion with binding so a deleted token cannot retain audio access.
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if err := s.mcpTokens.Delete(c.Request.Context(), c.Param("id")); err != nil {
		mcpManagementError(c, err)
		return
	}
	s.mcpConnections.Revoke(c.Param("id"))
	c.Status(http.StatusNoContent)
}
func (s *Server) handleMCPConnectionBind(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}
	var req struct {
		TokenID string `json:"token_id" binding:"required"`
		Name    string `json:"name" binding:"required"`
		MediaID string `json:"media_id" binding:"required"`
		Lease   string `json:"lease" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要令牌、连接名称及网页媒体凭据"})
		return
	}
	// Bind atomically with respect to disabling MCP and revoking its token.
	s.mcpMu.RLock()
	defer s.mcpMu.RUnlock()
	if s.fullCfg == nil || !s.fullCfg.MCP.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "请先在设置页开启 MCP"})
		return
	}
	if _, err := s.mcpTokens.Active(c.Request.Context(), req.TokenID); err != nil {
		mcpManagementError(c, err)
		return
	}
	connection, err := s.mcpConnections.Bind(mcpserver.Connection{ID: c.Param("id"), TokenID: req.TokenID, Name: strings.TrimSpace(req.Name)}, mcpserver.AudioCredentials{Owner: s.authSnapshot().Username, MediaID: req.MediaID, Lease: req.Lease})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, connection)
}
func (s *Server) handleMCPConnectionDelete(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}
	if err := s.mcpConnections.Delete(c.Param("id"), s.authSnapshot().Username); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
func mcpManagementError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, mcpserver.ErrUnauthorized) {
		status = http.StatusNotFound
	}
	c.JSON(status, gin.H{"error": err.Error()})
}
