package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yibaiba/hideck/internal/db"

	"github.com/gin-gonic/gin"
)

func (s *Server) useGetSMSContacts(c businessContext) {
	deviceID := c.Query("device_id")
	imsi := c.Query("imsi")

	limitStr := c.DefaultQuery("limit", "50")
	var limit int
	fmt.Sscanf(limitStr, "%d", &limit)

	var beforeTs *time.Time
	if v := strings.TrimSpace(c.Query("before_ts")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			beforeTs = &t
		}
	}
	beforePeer := strings.TrimSpace(c.Query("before_peer"))

	var iccid string
	if strings.TrimSpace(deviceID) != "" {
		resolved, status, msg := s.resolveSMSICCID(deviceID, imsi)
		if status != 0 {
			c.JSON(status, gin.H{"status": "error", "message": msg})
			return
		}
		iccid = resolved
	}

	var contacts []db.SMSContact
	var err error
	if iccid != "" {
		contacts, err = db.GetSMSContactsByICCID(iccid, limit, beforeTs, beforePeer)
	} else {
		contacts, err = db.GetSMSContacts(limit, beforeTs, beforePeer)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "查询数据库失败: " + err.Error()})
		return
	}

	iccidDevice := s.smsDeviceInfoByICCID()

	phoneIndex, err := db.GetSIMPhoneNumberIndex()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "查询本机号码失败: " + err.Error()})
		return
	}

	enriched := make([]SMSContactWithDevice, 0, len(contacts))
	for _, ct := range contacts {
		info := iccidDevice[ct.ICCID]
		enriched = append(enriched, SMSContactWithDevice{
			SMSContact: ct,
			DeviceID:   info.id,
			DeviceName: info.name,
			LocalPhone: smsContactLocalPhone(ct, phoneIndex),
		})
	}

	c.JSON(http.StatusOK, enriched)
}

func (s *Server) useGetSMSThread(c businessContext) {
	deviceID := c.Query("device_id")
	imsi := c.Query("imsi")
	requestedICCID := db.CanonicalICCID(c.Query("iccid"))
	peer := strings.TrimSpace(c.Query("peer"))
	if peer == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少 peer 参数"})
		return
	}

	limitStr := c.DefaultQuery("limit", "50")
	var limit int
	fmt.Sscanf(limitStr, "%d", &limit)

	var beforeTs *time.Time
	if v := strings.TrimSpace(c.Query("before_ts")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			beforeTs = &t
		}
	}
	var beforeID uint
	if v := strings.TrimSpace(c.Query("before_id")); v != "" {
		var parsed uint64
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			beforeID = uint(parsed)
		}
	}

	iccid := requestedICCID
	if requestedICCID != "" && (strings.TrimSpace(deviceID) != "" || strings.TrimSpace(imsi) != "") {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "iccid 不能与 device_id 或 imsi 同时使用"})
		return
	}
	if requestedICCID == "" && (strings.TrimSpace(deviceID) != "" || strings.TrimSpace(imsi) != "") {
		resolved, status, msg := s.resolveSMSICCID(deviceID, imsi)
		if status != 0 {
			c.JSON(status, gin.H{"status": "error", "message": msg})
			return
		}
		iccid = resolved
	}

	list, err := db.GetSMSByICCIDAndPeer(iccid, peer, limit, beforeTs, beforeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "查询数据库失败: " + err.Error()})
		return
	}

	devName := s.smsDeviceInfoByICCID()[iccid].name

	enriched := make([]SMSWithDevice, 0, len(list))
	for _, sms := range list {
		enriched = append(enriched, SMSWithDevice{
			SMS:        sms,
			DeviceName: devName,
		})
	}

	c.JSON(http.StatusOK, enriched)
}

func (s *Server) useDeleteSMSMessage(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	id64, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id64 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "无效的短信 id"})
		return
	}

	threadEmpty, imsi, peer, err := db.DeleteSMSByID(uint(id64))
	if err != nil {
		if errors.Is(err, db.ErrSMSNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "短信不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "删除短信失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":       "ok",
		"thread_empty": threadEmpty,
		"imsi":         imsi,
		"peer":         peer,
	})
}

func (s *Server) useDeleteSMSThread(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	deviceID := c.Query("device_id")
	imsi := c.Query("imsi")
	iccid := db.CanonicalICCID(c.Query("iccid"))
	peer := strings.TrimSpace(c.Query("peer"))
	if peer == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "缺少 peer 参数"})
		return
	}

	resolved := iccid
	if iccid != "" && (strings.TrimSpace(deviceID) != "" || strings.TrimSpace(imsi) != "") {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "iccid 不能与 device_id 或 imsi 同时使用"})
		return
	}
	if iccid == "" {
		var status int
		var msg string
		resolved, status, msg = s.resolveSMSICCID(deviceID, imsi)
		if status != 0 {
			c.JSON(status, gin.H{"status": "error", "message": msg})
			return
		}
	}

	deleted, err := db.DeleteSMSByICCIDAndPeer(resolved, peer)
	if err != nil {
		if errors.Is(err, db.ErrSMSNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "短信会话不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "删除短信会话失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"deleted": deleted,
		"iccid":   resolved,
		"peer":    peer,
	})
}
