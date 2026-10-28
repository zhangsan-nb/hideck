package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/global"
	"github.com/yibaiba/hideck/internal/mcpserver"
	"github.com/yibaiba/hideck/pkg/logger"
)

func (s *Server) initializeMCP() {
	if db.DB != nil {
		s.mcpTokens = mcpserver.NewTokens(db.NewMCPTokenStore(db.DB))
	}
	s.mcpConnections = mcpserver.NewConnections(func(credentials mcpserver.AudioCredentials) error {
		return s.phone.ValidateBrowserMedia(credentials.Owner, credentials.MediaID, credentials.Lease)
	})
}

func (s *Server) serveMCP(c *gin.Context) {
	if !s.requireMCPManagement(c) {
		return
	}
	s.ensureMCPHandler()
	if s.mcpError != nil {
		logger.Error("MCP 初始化失败", "err", s.mcpError)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "MCP 初始化失败: " + s.mcpError.Error()})
		return
	}
	s.mcpHandler.ServeHTTP(c.Writer, c.Request)
}

func (s *Server) ensureMCPHandler() {
	s.mcpOnce.Do(func() { s.mcpHandler, s.mcpError = s.newMCPHandler() })
}

func (s *Server) newMCPHandler() (*mcpserver.Handler, error) {
	raw, err := loadOpenAPISpecJSON()
	if err != nil {
		return nil, err
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	tools, err := s.mcpBusinessTools(spec)
	if err != nil {
		return nil, err
	}
	tools = append(tools, s.mcpFileTools()...)
	tools = append(tools, mcpserver.Tool{Name: "hideck_phone_audio_connections", Description: "查询当前令牌获准使用的网页音频连接。返回连接 ID、名称和状态。", Schema: json.RawMessage(`{"type":"object","additionalProperties":false}`), ReadOnly: true,
		Execute: func(ctx context.Context, _ json.RawMessage, _ mcpserver.Progress) (mcpserver.Result, error) {
			return mcpserver.Result{Data: map[string]any{"connections": s.mcpConnections.List(mcpserver.TokenID(ctx))}}, nil
		}})
	for i, tool := range tools {
		prepared, err := s.prepareMCPTool(tool)
		if err != nil {
			return nil, err
		}
		tools[i] = prepared
	}
	return mcpserver.New(mcpserver.Options{Tokens: s.mcpTokens, Enabled: s.mcpEnabled, Tools: tools,
		ReadRecording: s.readMCPRecording, Logger: slog.New(logger.NewSlogHandler(logger.ZapLogger())), Version: global.Version, Instructions: mcpInstructions})
}

func (s *Server) mcpBusinessTools(spec map[string]any) ([]mcpserver.Tool, error) {
	tools := []mcpserver.Tool{}
	for _, operation := range s.mcpOperations() {
		schema, description, err := mcpInputSchema(spec, operation)
		if err != nil {
			return nil, err
		}
		output, err := mcpOutputSchema(spec, operation)
		if err != nil {
			return nil, err
		}
		tools = append(tools, mcpserver.Tool{Name: operation.name, Description: description + mcpToolGuidance(operation.name), Schema: schema, OutputSchema: output,
			ReadOnly: operation.readOnly, Destructive: operation.destructive,
			Execute: func(ctx context.Context, raw json.RawMessage, progress mcpserver.Progress) (mcpserver.Result, error) {
				return s.executeMCPBusiness(ctx, operation, mcpArguments{raw: raw, progress: progress})
			}})
	}
	return tools, nil
}

func (s *Server) prepareMCPTool(tool mcpserver.Tool) (mcpserver.Tool, error) {
	prepared := tool
	if len(prepared.OutputSchema) == 0 {
		output, err := mcpSpecialOutput(prepared.Name)
		if err != nil {
			return prepared, err
		}
		prepared.OutputSchema = output
		prepared.Description += mcpToolGuidance(prepared.Name)
	}
	execute := prepared.Execute
	prepared.Execute = func(ctx context.Context, raw json.RawMessage, progress mcpserver.Progress) (mcpserver.Result, error) {
		_, accepted, err := s.disclaimerAcceptance(ctx)
		if err != nil {
			return mcpserver.Result{}, err
		}
		if !accepted {
			return mcpserver.Result{}, errors.New("请先在网页阅读并确认当前版本的免责声明")
		}
		return execute(ctx, raw, progress)
	}
	return prepared, nil
}

type mcpArguments struct {
	raw      json.RawMessage
	progress mcpserver.Progress
}

func (s *Server) executeMCPBusiness(ctx context.Context, operation mcpOperation, input mcpArguments) (mcpserver.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var args map[string]json.RawMessage
	if err := json.Unmarshal(input.raw, &args); err != nil {
		return mcpserver.Result{}, err
	}
	body := args["request"]
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	call := &businessCall{ctx: ctx, arguments: args, body: body, headers: make(http.Header), status: http.StatusOK, progress: input.progress, cancel: cancel}
	if operation.audio {
		credentials, err := s.mcpConnections.Resolve(mcpserver.TokenID(ctx), call.Query("audio_connection_id"))
		if err != nil {
			return mcpserver.Result{}, err
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			return mcpserver.Result{}, err
		}
		request["media_id"] = credentials.MediaID
		call.body, _ = json.Marshal(request)
		call.headers.Set("X-Phone-Lease", credentials.Lease)
	}
	if s.pool == nil && mcpRequiresPool(operation.name) {
		return mcpserver.Result{}, errors.New("设备池未初始化")
	}
	operation.run(call)
	if call.progressErr != nil {
		return mcpserver.Result{}, call.progressErr
	}
	if err := ctx.Err(); err != nil {
		return mcpserver.Result{}, err
	}
	return mcpBusinessResult(call)
}
func mcpRequiresPool(name string) bool {
	for _, domain := range []string{"device_", "network_", "esim_", "sms_send", "phone_configure", "phone_reconnect"} {
		if strings.HasPrefix(name, "hideck_"+domain) {
			return true
		}
	}
	return false
}
func mcpBusinessResult(call *businessCall) (mcpserver.Result, error) {
	raw, err := json.Marshal(businessStatus(call))
	if err != nil {
		return mcpserver.Result{}, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return mcpserver.Result{}, err
	}
	resources := []*mcp.ResourceLink{}
	sanitizeMCPData(data, &resources)
	failed := call.status >= http.StatusBadRequest
	if body, ok := data["data"].(map[string]any); ok {
		message, _ := body["error"].(string)
		failed = failed || message != "" || body["status"] == "error"
	}
	return mcpserver.Result{Data: data, Failed: failed, Resources: resources}, nil
}
func sanitizeMCPData(value any, resources *[]*mcp.ResourceLink) {
	switch node := value.(type) {
	case map[string]any:
		for key, value := range node {
			// Recording artifacts are exposed through resources. Device binding
			// paths (USB, control ports and QMI sockets) are business identifiers.
			if key == "audio_path" || key == "source_audio_path" || key == "pcap_path" || key == "recording_path" {
				delete(node, key)
				if path, ok := value.(string); ok {
					addMCPRecording(node, filepath.Base(path), resources)
				}
				continue
			}
			if key == "recording_name" || key == "recording" {
				if name, ok := value.(string); ok {
					addMCPRecording(node, name, resources)
				}
			}
			sanitizeMCPData(value, resources)
		}
	case []any:
		for _, item := range node {
			sanitizeMCPData(item, resources)
		}
	}
}
func addMCPRecording(node map[string]any, name string, resources *[]*mcp.ResourceLink) {
	if !validVoiceRecordingName(name) {
		return
	}
	uri := "hideck://recordings/" + name
	node["recording_uri"] = uri
	*resources = append(*resources, &mcp.ResourceLink{URI: uri, Name: name, MIMEType: "audio/mpeg"})
}
func (s *Server) readMCPRecording(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validVoiceRecordingName(name) {
		return nil, errors.New("录音文件名无效")
	}
	if strings.TrimSpace(s.voiceRecordingDirectory) == "" {
		return nil, errors.New("通话录音目录未配置")
	}
	root, err := os.OpenRoot(s.voiceRecordingDirectory)
	if err != nil {
		return nil, errors.New("通话录音目录不可用")
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, errors.New("通话录音不存在或不可读")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, errors.New("通话录音不可读")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("读取通话录音失败")
	}
	return data, ctx.Err()
}
