package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tokenKey struct{}
type requestContextKey struct{}

func TokenID(ctx context.Context) string { id, _ := ctx.Value(tokenKey{}).(string); return id }

// A negative progress value reports another completed stage with an unknown total.
type Progress func(string, float64) error
type Result struct {
	Data      any
	Failed    bool
	Resources []*mcp.ResourceLink
}
type Tool struct {
	Name         string
	Description  string
	Schema       json.RawMessage
	OutputSchema json.RawMessage
	ReadOnly     bool
	Destructive  bool
	Idempotent   bool
	Execute      func(context.Context, json.RawMessage, Progress) (Result, error)
}
type Options struct {
	Tokens        *Tokens
	Enabled       func() bool
	Tools         []Tool
	ReadRecording func(context.Context, string) ([]byte, error)
	Logger        *slog.Logger
	Version       string
	Instructions  string
}

type ServerInfo struct {
	Version               string `json:"version"`
	ToolCount             int    `json:"tool_count"`
	OutputSchemaCount     int    `json:"output_schema_count"`
	InstructionsAvailable bool   `json:"instructions_available"`
}

type Handler struct {
	http.Handler
	info ServerInfo
}

func (h *Handler) Info() ServerInfo { return h.info }

func New(options Options) (*Handler, error) {
	if options.Version == "" {
		options.Version = "1.0.0"
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "hideck", Version: options.Version}, &mcp.ServerOptions{Instructions: options.Instructions})
	info := ServerInfo{Version: options.Version, ToolCount: len(options.Tools), InstructionsAvailable: options.Instructions != ""}
	for _, tool := range options.Tools {
		if err := register(server, tool, options); err != nil {
			return nil, err
		}
		if len(tool.OutputSchema) > 0 {
			info.OutputSchemaCount++
		}
	}
	if options.ReadRecording != nil {
		server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "hideck://recordings/{name}", Name: "通话录音", MIMEType: "audio/mpeg"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			if err := authorizeCall(ctx, options); err != nil {
				return nil, err
			}
			data, err := options.ReadRecording(ctx, strings.TrimPrefix(req.Params.URI, "hideck://recordings/"))
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "audio/mpeg", Blob: data}}}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: -1})
	protected := http.NewCrossOriginProtection().Handler(handler)
	return &Handler{Handler: authenticate(protected, options), info: info}, nil
}

func authenticate(next http.Handler, options Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !options.Enabled() {
			http.Error(w, "MCP 服务已关闭", http.StatusServiceUnavailable)
			return
		}
		scheme, secret, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			rejectAuthentication(w)
			return
		}
		token, err := options.Tokens.Authenticate(r.Context(), secret)
		if errors.Is(err, ErrUnauthorized) {
			rejectAuthentication(w)
			return
		}
		if err != nil {
			http.Error(w, "MCP 令牌存储读取失败", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), tokenKey{}, token.ID)
		ctx = context.WithValue(ctx, requestContextKey{}, r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func rejectAuthentication(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, ErrUnauthorized.Error(), http.StatusUnauthorized)
}
func authorizeCall(ctx context.Context, options Options) error {
	if !options.Enabled() {
		return errors.New("MCP 服务已关闭")
	}
	_, err := options.Tokens.Active(ctx, TokenID(ctx))
	return err
}

func register(server *mcp.Server, tool Tool, options Options) error {
	var schema jsonschema.Schema
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		return fmt.Errorf("%s schema: %w", tool.Name, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("%s schema: %w", tool.Name, err)
	}
	output, err := resolveOutputSchema(tool)
	if err != nil {
		return err
	}
	metadata := &mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: &schema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: tool.ReadOnly, DestructiveHint: &tool.Destructive, IdempotentHint: tool.Idempotent}}
	if output.schema != nil {
		metadata.OutputSchema = output.schema
	}
	invocation := registeredTool{tool: tool, options: options, input: resolved, output: output}
	server.AddTool(metadata, invocation.call)
	return nil
}
