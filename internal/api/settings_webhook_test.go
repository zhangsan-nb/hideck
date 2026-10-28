package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWebhookTestEndpointUsesPayloadTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var received map[string]string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode webhook body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	body := webhookTestRequestBody(t, target.URL, `{"title":{{title}},"desp":{{message}}}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/settings/notifications/webhook/test", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Server{}).handleTestWebhookNotification(ctx)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if received["title"] == "" || received["desp"] != "这是一条 Webhook 测试通知" {
		t.Fatalf("payload = %#v", received)
	}
}

func TestWebhookTestEndpointRejectsInvalidPayloadTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer target.Close()

	body := webhookTestRequestBody(t, target.URL, `{"title":{{unknown}}}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/settings/notifications/webhook/test", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Server{}).handleTestWebhookNotification(ctx)

	if recorder.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, calls.Load(), recorder.Body.String())
	}
}

func webhookTestRequestBody(t *testing.T, targetURL, payloadTemplate string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"enabled": true, "urls": []string{targetURL}, "timeout_ms": 1000,
		"retry_max": 0, "text_template": "{{text}}", "payload_template": payloadTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
