package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/mcpserver"
)

type mcpTokenDiagnostic struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Revoked  bool                `json:"revoked"`
	Activity *mcpserver.Activity `json:"activity"`
}

type mcpDiagnostics struct {
	mcpserver.ServerInfo
	Enabled            bool                 `json:"enabled"`
	Initialized        bool                 `json:"initialized"`
	DisclaimerAccepted bool                 `json:"disclaimer_accepted"`
	Ready              bool                 `json:"ready"`
	Status             string               `json:"status"`
	Message            string               `json:"message"`
	CheckedAt          time.Time            `json:"checked_at"`
	Tokens             []mcpTokenDiagnostic `json:"tokens"`
}

func (s *Server) handleMCPDiagnostics(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !s.requireMCPManagement(c) {
		return
	}
	diagnostics, err := s.inspectMCP(c.Request.Context())
	if err != nil {
		mcpManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, diagnostics)
}

func (s *Server) inspectMCP(ctx context.Context) (mcpDiagnostics, error) {
	result := mcpDiagnostics{Enabled: s.mcpEnabled(), CheckedAt: time.Now().UTC(), Tokens: []mcpTokenDiagnostic{}}
	tokens, err := s.mcpTokens.List(ctx)
	if err != nil {
		return result, err
	}
	active := 0
	for _, token := range tokens {
		if token.RevokedAt == nil {
			active++
		}
		result.Tokens = append(result.Tokens, mcpTokenDiagnostic{ID: token.ID, Name: token.Name, Revoked: token.RevokedAt != nil, Activity: s.mcpTokens.Activity(token.ID)})
	}
	_, result.DisclaimerAccepted, err = s.disclaimerAcceptance(ctx)
	if err != nil {
		return result, err
	}
	s.ensureMCPHandler()
	if s.mcpError == nil {
		result.Initialized = true
		result.ServerInfo = s.mcpHandler.Info()
	}
	result.Status, result.Message = mcpReadiness(result, s.mcpError, active)
	result.Ready = result.Status == "ready"
	return result, nil
}

func mcpReadiness(result mcpDiagnostics, initError error, active int) (string, string) {
	switch {
	case initError != nil:
		return "initialization_failed", "工具初始化失败：" + initError.Error()
	case !result.Enabled:
		return "disabled", "MCP 已关闭，请先开启服务。"
	case !result.DisclaimerAccepted:
		return "disclaimer_required", "请先在网页确认当前版本的免责声明。"
	case active == 0:
		return "token_required", "尚无有效令牌，请创建令牌后配置客户端。"
	default:
		return "ready", "服务已就绪；客户端是否连通请查看实际请求和调用记录。"
	}
}
