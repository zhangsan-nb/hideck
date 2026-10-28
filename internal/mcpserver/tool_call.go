package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type registeredTool struct {
	tool    Tool
	options Options
	input   *jsonschema.Resolved
	output  outputContract
}

func (invocation registeredTool) call(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	result, err := invocation.invoke(ctx, req)
	if err == nil && !result.IsError && invocation.output.resolved != nil {
		if validation := validateSerializedOutput(result, invocation.output.resolved); validation != nil {
			result = failure(fmt.Errorf("%s 输出不符合契约；操作可能已执行，请查询当前状态，不要直接重试", invocation.tool.Name))
		}
	}
	activity := ToolActivity{Name: invocation.tool.Name, At: time.Now().UTC(), DurationMS: time.Since(start).Milliseconds(), Failed: err != nil || result.IsError}
	invocation.record(ctx, req, activity)
	return result, err
}

func (invocation registeredTool) record(ctx context.Context, req *mcp.CallToolRequest, activity ToolActivity) {
	invocation.options.Tokens.recordTool(TokenID(ctx), activity)
	if invocation.options.Logger == nil {
		return
	}
	var args struct {
		DeviceID string `json:"device_id"`
		Request  struct {
			DeviceID string `json:"device_id"`
		} `json:"request"`
	}
	_ = json.Unmarshal(req.Params.Arguments, &args)
	if args.DeviceID == "" {
		args.DeviceID = args.Request.DeviceID
	}
	invocation.options.Logger.Info("MCP tool", "tool", activity.Name, "token_id", TokenID(ctx), "device_id", args.DeviceID, "duration_ms", activity.DurationMS, "failed", activity.Failed)
}

func (invocation registeredTool) invoke(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tool, options, schema := invocation.tool, invocation.options, invocation.input
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if original, ok := ctx.Value(requestContextKey{}).(context.Context); ok {
		stop := context.AfterFunc(original, cancel)
		defer stop()
	}
	if err := authorizeCall(ctx, options); err != nil {
		return failure(err), nil
	}
	// MCP permits tools/call to omit arguments for tools without inputs.
	raw := req.Params.Arguments
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var arguments map[string]any
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return failure(err), nil
	}
	if err := schema.Validate(arguments); err != nil {
		return failure(fmt.Errorf("参数不符合工具契约: %w", err)), nil
	}
	progress := requestProgress(ctx, req)
	result, err := tool.Execute(ctx, raw, progress)
	if err != nil {
		return failure(err), nil
	}
	payload, err := json.Marshal(result.Data)
	if err != nil {
		return nil, err
	}
	response := &mcp.CallToolResult{StructuredContent: result.Data, IsError: result.Failed, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}
	for _, resource := range result.Resources {
		response.Content = append(response.Content, resource)
	}
	return response, nil
}
func failure(err error) *mcp.CallToolResult {
	data := map[string]any{"error": err.Error()}
	payload, _ := json.Marshal(data)
	return &mcp.CallToolResult{IsError: true, StructuredContent: data, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}
}

func requestProgress(ctx context.Context, req *mcp.CallToolRequest) Progress {
	var stages float64
	return func(message string, percent float64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		token := req.Params.GetProgressToken()
		if token == nil {
			return nil
		}
		total := float64(100)
		if percent < 0 {
			stages++
			percent = stages
			total = 0
		}
		return req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Message: message, Progress: percent, Total: total})
	}
}
