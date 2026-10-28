package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/webdavarchive"
)

type archiveSettingsRequest struct {
	Enabled           bool   `json:"enabled"`
	Endpoint          string `json:"endpoint"`
	Username          string `json:"username"`
	Password          string `json:"password"`
	ClearPassword     bool   `json:"clear_password"`
	BasePath          string `json:"base_path"`
	IntervalHours     int    `json:"interval_hours"`
	RetentionDays     int    `json:"retention_days"`
	AutoCleanup       bool   `json:"auto_cleanup"`
	IncludeSMS        bool   `json:"include_sms"`
	IncludeCalls      bool   `json:"include_calls"`
	IncludeRecordings bool   `json:"include_recordings"`
}

type archiveSettingsResponse struct {
	Enabled           bool                 `json:"enabled"`
	Endpoint          string               `json:"endpoint"`
	Username          string               `json:"username"`
	PasswordSet       bool                 `json:"password_set"`
	BasePath          string               `json:"base_path"`
	IntervalHours     int                  `json:"interval_hours"`
	RetentionDays     int                  `json:"retention_days"`
	AutoCleanup       bool                 `json:"auto_cleanup"`
	IncludeSMS        bool                 `json:"include_sms"`
	IncludeCalls      bool                 `json:"include_calls"`
	IncludeRecordings bool                 `json:"include_recordings"`
	Status            webdavarchive.Status `json:"status"`
}

func (s *Server) handleGetArchiveSettings(c *gin.Context) {
	if s.archive == nil {
		archiveUnavailable(c)
		return
	}
	c.JSON(http.StatusOK, archiveSettingsPayload(s.archive.Config(), s.archive.Status()))
}

func (s *Server) handleUpdateArchiveSettings(c *gin.Context) {
	if s.archive == nil {
		archiveUnavailable(c)
		return
	}
	var request archiveSettingsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "参数错误"})
		return
	}

	s.archiveConfigMu.Lock()
	defer s.archiveConfigMu.Unlock()
	updated := mergeArchiveSettings(s.archive.Config(), request)
	if err := webdavarchive.ValidateRuntimeConfig(updated); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	fileConfig := archiveFileConfig(updated)
	if err := config.UpdateArchiveInFile(s.configPath, fileConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "写入归档配置失败: " + err.Error()})
		return
	}
	if err := s.archive.Configure(updated); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "应用归档配置失败: " + err.Error()})
		return
	}
	if s.fullCfg != nil {
		s.fullCfg.Archive = fileConfig
	}
	c.JSON(http.StatusOK, archiveSettingsPayload(updated, s.archive.Status()))
}

func (s *Server) handleTestArchiveSettings(c *gin.Context) {
	if s.archive == nil {
		archiveUnavailable(c)
		return
	}
	var request archiveSettingsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "参数错误"})
		return
	}
	if err := s.archive.Probe(c.Request.Context(), mergeArchiveSettings(s.archive.Config(), request)); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) handleRunArchive(c *gin.Context) {
	if s.archive == nil {
		archiveUnavailable(c)
		return
	}
	summary, err := s.archive.RunNow(c.Request.Context())
	if err != nil {
		archiveActionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "summary": summary})
}

func (s *Server) handlePreviewArchiveCleanup(c *gin.Context) {
	if s.archive == nil {
		archiveUnavailable(c)
		return
	}
	preview, err := s.archive.PreviewCleanup(c.Request.Context())
	if err != nil {
		archiveActionError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (s *Server) handleArchiveCleanup(c *gin.Context) {
	if s.archive == nil {
		archiveUnavailable(c)
		return
	}
	var request struct {
		Confirm      bool   `json:"confirm"`
		PreviewToken string `json:"preview_token"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm || strings.TrimSpace(request.PreviewToken) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "清理前必须先预览并明确确认"})
		return
	}
	preview, err := s.archive.Cleanup(c.Request.Context(), strings.TrimSpace(request.PreviewToken))
	if err != nil {
		archiveActionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cleaned": preview})
}

func mergeArchiveSettings(current webdavarchive.RuntimeConfig, request archiveSettingsRequest) webdavarchive.RuntimeConfig {
	password := current.Password
	if request.ClearPassword {
		password = ""
	} else if request.Password != "" {
		password = request.Password
	}
	return webdavarchive.RuntimeConfig{
		Enabled: request.Enabled, Endpoint: strings.TrimSpace(request.Endpoint),
		Username: strings.TrimSpace(request.Username), Password: password,
		BasePath: strings.TrimSpace(request.BasePath), Interval: time.Duration(request.IntervalHours) * time.Hour,
		RetentionDays: request.RetentionDays, AutoCleanup: request.AutoCleanup,
		IncludeSMS: request.IncludeSMS, IncludeCalls: request.IncludeCalls,
		IncludeRecordings: request.IncludeRecordings,
	}
}

func archiveSettingsPayload(runtime webdavarchive.RuntimeConfig, status webdavarchive.Status) archiveSettingsResponse {
	return archiveSettingsResponse{
		Enabled: runtime.Enabled, Endpoint: runtime.Endpoint, Username: runtime.Username,
		PasswordSet: runtime.Password != "", BasePath: runtime.BasePath,
		IntervalHours: int(runtime.Interval / time.Hour), RetentionDays: runtime.RetentionDays,
		AutoCleanup: runtime.AutoCleanup, IncludeSMS: runtime.IncludeSMS,
		IncludeCalls: runtime.IncludeCalls, IncludeRecordings: runtime.IncludeRecordings,
		Status: status,
	}
}

func archiveFileConfig(runtime webdavarchive.RuntimeConfig) config.ArchiveConfig {
	return config.ArchiveConfig{
		Enabled: runtime.Enabled, Endpoint: runtime.Endpoint, Username: runtime.Username,
		Password: runtime.Password, BasePath: runtime.BasePath,
		IntervalHours: int(runtime.Interval / time.Hour), RetentionDays: runtime.RetentionDays,
		AutoCleanup: runtime.AutoCleanup, IncludeSMS: runtime.IncludeSMS,
		IncludeCalls: runtime.IncludeCalls, IncludeRecordings: runtime.IncludeRecordings,
	}
}

func archiveActionError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, webdavarchive.ErrRunInProgress) {
		status = http.StatusConflict
	}
	if errors.Is(err, webdavarchive.ErrCleanupPreviewChanged) {
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"status": "error", "message": err.Error()})
}

func archiveUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "WebDAV 归档服务未初始化"})
}
