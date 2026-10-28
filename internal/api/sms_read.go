package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/pkg/logger"
)

type markSMSThreadReadRequest struct {
	ThroughID  uint   `json:"through_id,omitempty"`
	MessageIDs []uint `json:"message_ids,omitempty"`
}

func (request markSMSThreadReadRequest) valid() bool {
	if request.MessageIDs == nil {
		return request.ThroughID > 0
	}
	if request.ThroughID != 0 || len(request.MessageIDs) == 0 {
		return false
	}
	for _, id := range request.MessageIDs {
		if id == 0 {
			return false
		}
	}
	return true
}

func (s *Server) handleMarkSMSThreadRead(c *gin.Context) {
	s.useMarkSMSThreadRead(c)
}

func (s *Server) useMarkSMSThreadRead(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	iccid := db.CanonicalICCID(c.Query("iccid"))
	if iccid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少 iccid 参数"})
		return
	}
	peer := strings.TrimSpace(c.Query("peer"))
	if peer == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少 peer 参数"})
		return
	}
	var request markSMSThreadReadRequest
	if err := c.ShouldBindJSON(&request); err != nil || !request.valid() {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "必须提供正整数 through_id 或非空正整数数组 message_ids，且只能选择一种"})
		return
	}
	var result db.SMSReadResult
	var err error
	if request.MessageIDs != nil {
		result, err = db.MarkSMSMessagesReadByICCID(iccid, peer, request.MessageIDs)
	} else {
		result, err = db.MarkSMSThreadReadByICCID(iccid, peer, request.ThroughID)
	}
	if err != nil {
		if errors.Is(err, db.ErrSMSReadBoundaryInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "已读消息 ID 不属于当前短信会话"})
			return
		}
		if errors.Is(err, db.ErrSMSNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "短信会话不存在"})
			return
		}
		logger.Error("标记短信已读失败", "iccid", iccid, "peer", peer, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "标记短信已读失败"})
		return
	}
	response := gin.H{
		"status": "ok", "iccid": iccid, "peer": peer,
		"marked": result.Marked, "unread_count": result.UnreadCount,
	}
	if request.MessageIDs != nil {
		response["message_ids"] = request.MessageIDs
	} else {
		response["through_id"] = request.ThroughID
	}
	c.JSON(http.StatusOK, response)
}
