package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/db"
)

func (s *Server) handleSMSMessageTarget(c *gin.Context) {
	s.useSMSMessageTarget(c)
}

func (s *Server) useSMSMessageTarget(c businessContext) {
	id, err := strconv.ParseUint(c.Param("id"), 10, smsNotificationCursorBits)
	if err != nil || id == 0 || uint64(uint(id)) != id {
		c.JSON(http.StatusBadRequest, gin.H{"message": "短信 ID 必须是有效的正整数"})
		return
	}
	result, err := db.GetSMSMessageTarget(businessRequestContext(c), uint(id))
	if errors.Is(err, db.ErrSMSNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"message": "目标短信或会话已不存在，可能已被删除"})
		return
	}
	if err != nil {
		slog.Error("定位短信失败", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "目标短信加载失败，请稍后重试"})
		return
	}
	info := s.smsDeviceInfoByICCID()[result.Message.ICCID]
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"message":  SMSWithDevice{SMS: result.Message, DeviceName: info.name},
		"contact":  SMSContactWithDevice{SMSContact: result.Contact, DeviceID: info.id, DeviceName: info.name},
		"messages": result.Messages, "has_more": result.HasMore,
	})
}
