package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/device"
)

func (s *Server) classifyLebaraUKForDevice(ctx context.Context, deviceID string) (device.LebaraUKClass, error) {
	if s == nil || s.pool == nil {
		return device.LebaraUKClass{}, nil
	}
	return device.ClassifyWorkerLebaraUKForControl(ctx, s.pool.GetWorker(deviceID))
}

func (s *Server) classifyLebaraUKForICCID(ctx context.Context, iccid string) (device.LebaraUKClass, error) {
	if w := s.workerForICCID(iccid); w != nil {
		return device.ClassifyWorkerLebaraUKForControl(ctx, w)
	}
	return device.ClassifyLebaraUKForICCID(iccid, "")
}

func (s *Server) workerForICCID(iccid string) *device.Worker {
	if s == nil || s.pool == nil {
		return nil
	}
	iccid = db.CanonicalICCID(iccid)
	if iccid == "" {
		return nil
	}
	for _, w := range s.pool.GetAllWorkers() {
		if db.CanonicalICCID(w.CurrentICCID()) == iccid {
			return w
		}
	}
	return nil
}

func rejectLebaraUKRFUnlock(c businessContext, class device.LebaraUKClass, err error) bool {
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": fmt.Sprintf("识别 Lebara UK 射频策略失败: %v", err),
		})
		return true
	}
	if !class.IsLebara {
		return false
	}
	c.JSON(http.StatusConflict, gin.H{
		"status":  "error",
		"message": device.ErrLebaraUKRFLocked.Error(),
		"rf_lock": class.RFLock(),
	})
	return true
}

func (s *Server) handleEsimRecoverLebaraIdentity(c *gin.Context) {
	s.useEsimRecoverLebaraIdentity(c)
}

func (s *Server) useEsimRecoverLebaraIdentity(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	id := deviceIDParam(c)
	iccid := strings.TrimSpace(c.Param("iccid"))
	if iccid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "iccid 不能为空"})
		return
	}
	if s == nil || s.pool == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备未找到"})
		return
	}
	worker := s.pool.GetWorker(id)
	if worker == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备未找到或未运行"})
		return
	}
	err := s.pool.ScheduleLebaraUKIdentityRecover(worker, iccid, true)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "已开始恢复英国身份"})
		return
	}
	status := http.StatusConflict
	if errors.Is(err, device.ErrLebaraUKIdentityICCIDMismatch) {
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"status": "error", "message": err.Error()})
}

func writeLebaraUKRFLockError(c businessContext, err error) bool {
	if err == nil || !device.IsLebaraUKPolicyError(err) {
		return false
	}
	status := http.StatusConflict
	if errors.Is(err, device.ErrLebaraUKRFLocked) {
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"status": "error", "message": err.Error(), "rf_lock": device.RFLockLebaraUKNextGen})
	return true
}
