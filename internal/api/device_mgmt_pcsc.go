package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) handleDeviceMgmtRetryPCSCPIN(c *gin.Context) {
	s.useDeviceMgmtRetryPCSCPIN(c)
}

func (s *Server) useDeviceMgmtRetryPCSCPIN(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	id := deviceIDParam(c)
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "必须填写 id"})
		return
	}
	if s.pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "设备服务未就绪"})
		return
	}
	if err := s.pool.RetryPCSCPIN(businessRequestContext(c), id); err != nil {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "重新尝试 SIM PIN 失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "已允许当前 SIM 重新验证 PIN"})
}
