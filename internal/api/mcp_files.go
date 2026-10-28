package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/yibaiba/hideck/internal/esim"

	"github.com/yibaiba/hideck/internal/mbn"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

type mcpFileInput struct {
	Filename    string `json:"filename"`
	Content     string `json:"content_base64"`
	DeviceID    string `json:"device_id"`
	Model       string `json:"model"`
	Firmware    string `json:"firmware"`
	Source      string `json:"source"`
	ContentType string `json:"content_type"`
}

func (s *Server) mcpFileTools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: "hideck_esim_decode_activation", Description: "从二维码图片或 PDF 的 base64 文件内容识别 eSIM 激活码，沿用现有解码服务与 12 MiB 限制。", ReadOnly: true, Schema: json.RawMessage(`{"type":"object","required":["filename","content_base64","device_id"],"properties":{"filename":{"type":"string"},"content_base64":{"type":"string"},"content_type":{"type":"string"},"device_id":{"type":"string"}},"additionalProperties":false}`), Execute: s.mcpDecodeActivation},
		{Name: "hideck_phone_auto_answer_audio", Destructive: true, Description: "上传自动接听音频（WAV、AMR、AMR-WB、MP3），沿用网页 16 MiB 限制和转换流程。提供 base64 文件内容。", Schema: json.RawMessage(`{"type":"object","required":["filename","content_base64"],"properties":{"filename":{"type":"string"},"content_base64":{"type":"string"}},"additionalProperties":false}`), Execute: s.mcpAutoAnswerAudio},
		{Name: "hideck_contact_import", Destructive: true, Description: "导入 vcf 或 csv 联系人。提供文件名和 base64 文件内容，沿用网页导入规则与 8 MiB 限制。", Schema: json.RawMessage(`{"type":"object","required":["filename","content_base64"],"properties":{"filename":{"type":"string"},"content_base64":{"type":"string"},"device_id":{"type":"string"}},"additionalProperties":false}`), Execute: s.mcpImportContacts},
		{Name: "hideck_mbn_file_upload", Description: "上传原始 MBN 文件到现有文件库。提供 base64 文件内容，不接受服务器路径。", Schema: json.RawMessage(`{"type":"object","required":["filename","content_base64"],"properties":{"filename":{"type":"string"},"content_base64":{"type":"string"},"model":{"type":"string"},"firmware":{"type":"string"},"source":{"type":"string"}},"additionalProperties":false}`), Execute: s.mcpUploadMBN},
	}
}
func mcpDecodeFile(raw json.RawMessage) (mcpFileInput, []byte, error) {
	var input mcpFileInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return input, nil, err
	}
	if input.Filename == "" || filepath.Base(input.Filename) != input.Filename || strings.ContainsAny(input.Filename, `\/`) {
		return input, nil, errors.New("filename 必须是文件名，不接受宿主机路径")
	}
	content, err := base64.StdEncoding.DecodeString(input.Content)
	if err != nil {
		return input, nil, errors.New("content_base64 必须是有效的 base64 文件内容")
	}
	return input, content, nil
}
func (s *Server) mcpImportContacts(ctx context.Context, raw json.RawMessage, _ mcpserver.Progress) (mcpserver.Result, error) {
	input, data, err := mcpDecodeFile(raw)
	if err != nil {
		return mcpserver.Result{}, err
	}
	if len(data) > maxContactImportBytes {
		return mcpserver.Result{}, errors.New("导入文件不能超过 8MB")
	}
	result, err := s.importContactContent(ctx, contactImportInput{Name: input.Filename, DeviceID: input.DeviceID, Data: data})
	if err != nil {
		var failure *contactImportFailure
		if errors.As(err, &failure) {
			return mcpserver.Result{Failed: true, Data: map[string]any{"status_code": failure.Status, "data": map[string]any{"code": failure.Code, "message": failure.Error()}}}, nil
		}
		return mcpserver.Result{}, err
	}
	return mcpserver.Result{Data: map[string]any{"status_code": http.StatusOK, "data": result}}, nil
}
func (s *Server) mcpUploadMBN(ctx context.Context, raw json.RawMessage, _ mcpserver.Progress) (mcpserver.Result, error) {
	input, data, err := mcpDecodeFile(raw)
	if err != nil {
		return mcpserver.Result{}, err
	}
	if s.pool == nil || s.pool.MBNService() == nil {
		return mcpserver.Result{}, errors.New("MBN 服务未初始化")
	}
	asset, err := s.pool.MBNService().Upload(ctx, mbn.Asset{Name: input.Filename, Model: input.Model, Firmware: input.Firmware, Source: input.Source}, data)
	if err != nil {
		return mcpserver.Result{}, err
	}
	return mcpserver.Result{Data: map[string]any{"asset": asset}}, nil
}

func (s *Server) mcpAutoAnswerAudio(ctx context.Context, raw json.RawMessage, progress mcpserver.Progress) (mcpserver.Result, error) {
	input, data, err := mcpDecodeFile(raw)
	if err != nil {
		return mcpserver.Result{}, err
	}
	if len(data) > maxAutoAnswerAudioBytes {
		return mcpserver.Result{}, errors.New("自动接听录音不能超过 16 MiB")
	}
	call := &businessCall{ctx: ctx, headers: make(http.Header), status: http.StatusOK, progress: progress}
	if !s.requirePhone(call) {
		return mcpBusinessResult(call)
	}
	s.installAutoAnswerContent(call, input.Filename, bytes.NewReader(data))
	return mcpBusinessResult(call)
}

func (s *Server) mcpDecodeActivation(ctx context.Context, raw json.RawMessage, _ mcpserver.Progress) (mcpserver.Result, error) {
	input, data, err := mcpDecodeFile(raw)
	if err != nil {
		return mcpserver.Result{}, err
	}
	if s.pool == nil || s.pool.GetWorker(input.DeviceID) == nil {
		return mcpserver.Result{}, errors.New("设备未找到")
	}
	if len(data) > maxActivationUploadBytes {
		return mcpserver.Result{}, errors.New("文件太大，请换一张截图或更小的 PDF")
	}
	if err := ctx.Err(); err != nil {
		return mcpserver.Result{}, err
	}
	decoded, err := esim.DecodeActivationMedia(data, input.Filename, input.ContentType)
	return mcpserver.Result{Data: map[string]any{"activation": decoded}}, err
}
