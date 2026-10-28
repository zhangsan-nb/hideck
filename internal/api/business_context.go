package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// businessContext is the transport boundary for the existing domain use cases.
// Gin supplies it for REST; MCP supplies values directly, without HTTP loopback.
type businessContext interface {
	Param(string) string
	Query(string) string
	DefaultQuery(string, string) string
	GetQuery(string) (string, bool)
	GetHeader(string) string
	Get(any) (any, bool)
	ShouldBindJSON(any) error
	JSON(int, any)
	Status(int)
	Header(string, string)
	Data(int, string, []byte)
}

func businessRequestContext(c businessContext) context.Context {
	if rest, ok := c.(*gin.Context); ok {
		return rest.Request.Context()
	}
	return c.(*businessCall).ctx
}

func limitBusinessJSON(c businessContext, limit int64) {
	if rest, ok := c.(*gin.Context); ok {
		rest.Request.Body = http.MaxBytesReader(rest.Writer, rest.Request.Body, limit)
	}
}

type businessCall struct {
	ctx         context.Context
	arguments   map[string]json.RawMessage
	body        json.RawMessage
	headers     http.Header
	status      int
	result      any
	progress    func(string, float64) error
	progressErr error
	cancel      context.CancelFunc
}

func (c *businessCall) Param(key string) string { return c.Query(key) }
func (c *businessCall) Query(key string) string {
	raw := c.arguments[key]
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}
func (c *businessCall) GetQuery(key string) (string, bool) {
	_, ok := c.arguments[key]
	return c.Query(key), ok
}
func (c *businessCall) DefaultQuery(key, fallback string) string {
	if value, ok := c.GetQuery(key); ok {
		return value
	}
	return fallback
}
func (c *businessCall) GetHeader(key string) string     { return c.headers.Get(key) }
func (c *businessCall) Header(key, value string)        { c.headers.Set(key, value) }
func (c *businessCall) Get(key any) (any, bool)         { return nil, false }
func (c *businessCall) ShouldBindJSON(target any) error { return binding.JSON.BindBody(c.body, target) }
func (c *businessCall) JSON(status int, result any)     { c.status, c.result = status, result }
func (c *businessCall) Status(status int)               { c.status = status }
func (c *businessCall) Data(status int, mime string, data []byte) {
	c.JSON(status, map[string]any{"mime_type": mime, "content_base64": data})
}

func businessProgress(c businessContext, payload string) {
	if rest, ok := c.(*gin.Context); ok {
		if _, err := fmt.Fprintf(rest.Writer, "data: %s\n\n", payload); err != nil {
			_ = rest.Error(err)
		}
		rest.Writer.Flush()
		return
	}
	call := c.(*businessCall)
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		call.progressErr = err
		return
	}
	step, _ := event["step"].(string)
	if step == "error" {
		call.JSON(http.StatusBadGateway, event)
		return
	}
	if step == "done" {
		call.JSON(http.StatusOK, event)
		return
	}
	pct, _ := event["pct"].(float64)
	msg, _ := event["msg"].(string)
	if call.progressErr == nil {
		call.progressErr = call.progress(msg, pct)
		if call.progressErr != nil {
			call.cancel()
		}
	}
}

func businessStreamHeaders(c businessContext) {
	if rest, ok := c.(*gin.Context); ok {
		rest.Header("Content-Type", "text/event-stream")
		rest.Header("Cache-Control", "no-cache")
		rest.Header("Connection", "keep-alive")
		rest.Header("X-Accel-Buffering", "no")
	}
}

func businessStatus(c *businessCall) map[string]any {
	return map[string]any{"status_code": c.status, "data": c.result}
}
