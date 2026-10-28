package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/webdavarchive"
)

func TestArchiveSettingsRoutesMaskAndPreservePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("archive:\n  password: old-secret\ndevices: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &archiveManagerStub{config: validArchiveRuntimeConfig()}
	server := &Server{archive: manager, configPath: path, fullCfg: &config.Config{}}
	router := archiveTestRouter(server)

	getResponse := performArchiveRequest(t, router, http.MethodGet, "/api/settings/archive", "")
	if getResponse.Code != http.StatusOK || strings.Contains(getResponse.Body.String(), "old-secret") {
		t.Fatalf("GET status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	if !strings.Contains(getResponse.Body.String(), `"password_set":true`) {
		t.Fatalf("password state missing: %s", getResponse.Body.String())
	}

	payload := `{"enabled":true,"endpoint":"https://new.example/dav","username":"alice","password":"","base_path":"backup","interval_hours":12,"retention_days":60,"auto_cleanup":true,"include_sms":true,"include_calls":true,"include_recordings":false}`
	putResponse := performArchiveRequest(t, router, http.MethodPut, "/api/settings/archive", payload)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}
	if manager.config.Password != "old-secret" || manager.config.Endpoint != "https://new.example/dav" {
		t.Fatalf("runtime config = %+v", manager.config)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Archive.Password != "old-secret" || loaded.Archive.RetentionDays != 60 {
		t.Fatalf("persisted archive = %+v", loaded.Archive)
	}
}

func TestArchiveActionRoutesExposeFailuresAndRequireCleanupConfirmation(t *testing.T) {
	manager := &archiveManagerStub{
		config: validArchiveRuntimeConfig(), runErr: webdavarchive.ErrRunInProgress,
		preview: webdavarchive.CleanupPreview{Token: "preview-1", SMSCount: 2, CallCount: 1},
	}
	router := archiveTestRouter(&Server{archive: manager})
	runResponse := performArchiveRequest(t, router, http.MethodPost, "/api/archive/actions/run", "{}")
	if runResponse.Code != http.StatusConflict {
		t.Fatalf("run status=%d body=%s", runResponse.Code, runResponse.Body.String())
	}
	previewResponse := performArchiveRequest(t, router, http.MethodGet, "/api/archive/cleanup/preview", "")
	if previewResponse.Code != http.StatusOK || !strings.Contains(previewResponse.Body.String(), `"sms_count":2`) {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	cleanupResponse := performArchiveRequest(t, router, http.MethodPost, "/api/archive/cleanup", `{"confirm":false}`)
	if cleanupResponse.Code != http.StatusBadRequest || manager.cleanupCalls != 0 {
		t.Fatalf("cleanup status=%d calls=%d", cleanupResponse.Code, manager.cleanupCalls)
	}
	cleanupResponse = performArchiveRequest(t, router, http.MethodPost, "/api/archive/cleanup", `{"confirm":true}`)
	if cleanupResponse.Code != http.StatusBadRequest || manager.cleanupCalls != 0 {
		t.Fatalf("tokenless cleanup status=%d calls=%d", cleanupResponse.Code, manager.cleanupCalls)
	}
	cleanupResponse = performArchiveRequest(t, router, http.MethodPost, "/api/archive/cleanup", `{"confirm":true,"preview_token":"preview-1"}`)
	if cleanupResponse.Code != http.StatusOK || manager.cleanupCalls != 1 || manager.cleanupToken != "preview-1" {
		t.Fatalf("cleanup status=%d calls=%d token=%q", cleanupResponse.Code, manager.cleanupCalls, manager.cleanupToken)
	}
	manager.cleanupErr = webdavarchive.ErrCleanupPreviewChanged
	cleanupResponse = performArchiveRequest(t, router, http.MethodPost, "/api/archive/cleanup", `{"confirm":true,"preview_token":"preview-1"}`)
	if cleanupResponse.Code != http.StatusConflict || manager.cleanupCalls != 2 {
		t.Fatalf("stale cleanup status=%d calls=%d", cleanupResponse.Code, manager.cleanupCalls)
	}
}

type archiveManagerStub struct {
	config       webdavarchive.RuntimeConfig
	status       webdavarchive.Status
	runErr       error
	preview      webdavarchive.CleanupPreview
	cleanupCalls int
	cleanupToken string
	cleanupErr   error
}

func (stub *archiveManagerStub) Config() webdavarchive.RuntimeConfig { return stub.config }
func (stub *archiveManagerStub) Status() webdavarchive.Status        { return stub.status }
func (stub *archiveManagerStub) Configure(value webdavarchive.RuntimeConfig) error {
	stub.config = value
	return nil
}
func (stub *archiveManagerStub) Probe(context.Context, webdavarchive.RuntimeConfig) error { return nil }
func (stub *archiveManagerStub) RunNow(context.Context) (webdavarchive.RunSummary, error) {
	return webdavarchive.RunSummary{}, stub.runErr
}
func (stub *archiveManagerStub) PreviewCleanup(context.Context) (webdavarchive.CleanupPreview, error) {
	return stub.preview, nil
}
func (stub *archiveManagerStub) Cleanup(_ context.Context, token string) (webdavarchive.CleanupPreview, error) {
	stub.cleanupCalls++
	stub.cleanupToken = token
	return stub.preview, stub.cleanupErr
}
func (stub *archiveManagerStub) Stop(context.Context) error { return nil }

func validArchiveRuntimeConfig() webdavarchive.RuntimeConfig {
	return webdavarchive.RuntimeConfig{
		Endpoint: "https://dav.example.test", Username: "user", Password: "old-secret",
		BasePath: "hideck", Interval: 24 * time.Hour, RetentionDays: 30,
		IncludeSMS: true, IncludeCalls: true, IncludeRecordings: true,
	}
}

func archiveTestRouter(server *Server) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/settings/archive", server.handleGetArchiveSettings)
	router.PUT("/api/settings/archive", server.handleUpdateArchiveSettings)
	router.POST("/api/archive/actions/run", server.handleRunArchive)
	router.GET("/api/archive/cleanup/preview", server.handlePreviewArchiveCleanup)
	router.POST("/api/archive/cleanup", server.handleArchiveCleanup)
	return router
}

func performArchiveRequest(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

var _ archiveManager = (*archiveManagerStub)(nil)
