package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var webhookPlaceholderPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)

var webhookPayloadTemplateVariables = map[string]struct{}{
	"event": {}, "title": {}, "message": {}, "text": {}, "timestamp": {}, "time": {},
	"content": {}, "number": {}, "local_number": {}, "contact_name": {}, "attribution": {},
	"device_id": {}, "device_name": {}, "device_label": {},
}

func ValidateWebhookPayloadTemplate(template string) error {
	if strings.TrimSpace(template) == "" {
		return nil
	}
	_, err := renderWebhookPayloadTemplate(template, NotificationContext{
		Event: "test", Text: "HiDeck notification test", Timestamp: time.Unix(0, 0),
	})
	return err
}

func (w *WebhookChannel) renderPayload(ctx NotificationContext) ([]byte, error) {
	if strings.TrimSpace(w.payloadTemplate) != "" {
		return renderWebhookPayloadTemplate(w.payloadTemplate, ctx)
	}
	body, err := json.Marshal(defaultWebhookPayload(ctx))
	if err != nil {
		return nil, fmt.Errorf("序列化 webhook payload 失败: %w", err)
	}
	return body, nil
}

func defaultWebhookPayload(ctx NotificationContext) webhookPayload {
	timestamp := ctx.Timestamp.Format(time.RFC3339)
	return webhookPayload{
		Event: ctx.Event, Timestamp: timestamp, Text: ctx.Text,
		Meta: webhookPayloadMeta{
			DeviceID: strings.TrimSpace(ctx.DeviceID), DeviceName: strings.TrimSpace(ctx.DeviceName),
			DeviceLabel: ctx.DeviceLabel(), Number: strings.TrimSpace(ctx.Number),
			LocalNumber: strings.TrimSpace(ctx.LocalNumber), ContactName: strings.TrimSpace(ctx.ContactName),
			Attribution: strings.TrimSpace(ctx.Attribution), Event: ctx.Event, Timestamp: timestamp,
		},
	}
}

func renderWebhookPayloadTemplate(template string, ctx NotificationContext) ([]byte, error) {
	values := webhookPayloadTemplateValues(ctx)
	var renderErr error
	rendered := webhookPlaceholderPattern.ReplaceAllStringFunc(template, func(token string) string {
		name := webhookPlaceholderPattern.FindStringSubmatch(token)[1]
		if _, supported := webhookPayloadTemplateVariables[name]; !supported {
			renderErr = fmt.Errorf("Webhook JSON 模板包含不支持的变量 %q", name)
			return token
		}
		encoded, err := json.Marshal(values[name])
		if err != nil {
			renderErr = fmt.Errorf("编码 Webhook 模板变量 %q 失败: %w", name, err)
			return token
		}
		return string(encoded)
	})
	if renderErr != nil {
		return nil, renderErr
	}
	if strings.Contains(rendered, "{{") {
		return nil, errors.New("Webhook JSON 模板包含无法识别的变量")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil || len(payload) == 0 {
		return nil, errors.New("Webhook JSON 模板必须渲染为非空对象")
	}
	return []byte(rendered), nil
}

func webhookPayloadTemplateValues(ctx NotificationContext) map[string]string {
	return map[string]string{
		"event": strings.TrimSpace(ctx.Event), "title": ctx.NotificationTitle(),
		"message": strings.TrimSpace(ctx.Text), "text": strings.TrimSpace(ctx.Text),
		"timestamp": ctx.Timestamp.Format(time.RFC3339), "time": ctx.Timestamp.Local().Format("2006-01-02 15:04:05"),
		"content": ctx.Content, "number": ctx.Number, "local_number": ctx.LocalNumber,
		"contact_name": ctx.ContactName, "attribution": ctx.Attribution,
		"device_id": strings.TrimSpace(ctx.DeviceID), "device_name": strings.TrimSpace(ctx.DeviceName),
		"device_label": ctx.DeviceLabel(),
	}
}

func (w *WebhookChannel) renderText(ctx NotificationContext) (rendered string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("模板渲染异常: %v", recovered)
		}
	}()
	values := map[string]string{
		"text": strings.TrimSpace(ctx.Text), "event": strings.TrimSpace(ctx.Event),
		"timestamp": ctx.Timestamp.Format(time.RFC3339), "number": strings.TrimSpace(ctx.Number),
		"local_number": strings.TrimSpace(ctx.LocalNumber), "contact_name": strings.TrimSpace(ctx.ContactName),
		"attribution": strings.TrimSpace(ctx.Attribution), "device_id": strings.TrimSpace(ctx.DeviceID),
		"device_name": strings.TrimSpace(ctx.DeviceName), "device_label": ctx.DeviceLabel(),
	}
	rendered = webhookPlaceholderPattern.ReplaceAllStringFunc(w.textTemplate, func(token string) string {
		matches := webhookPlaceholderPattern.FindStringSubmatch(token)
		if len(matches) == 2 {
			if value, ok := values[matches[1]]; ok {
				return value
			}
		}
		return token
	})
	return rendered, nil
}
