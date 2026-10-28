package api

import (
	"net/http"

	"github.com/yibaiba/hideck/internal/device"

	"github.com/gin-gonic/gin"
)

func (s *Server) useDeviceMgmtStartNetwork(c businessContext) {
	deviceID := deviceIDParam(c)
	if deviceID == "" || s == nil || s.pool == nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "设备参数无效"})
		return
	}
	releaseMutation := s.pool.AcquireManagedDeviceMutation(deviceID)
	defer releaseMutation()
	s.useDeviceMgmtStartNetworkLocked(c, deviceID)
}

func (s *Server) useDeviceMgmtStartNetworkLocked(c businessContext, deviceID string) {
	worker := s.pool.GetWorker(deviceID)
	if worker == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备未找到"})
		return
	}
	nc := worker.NetworkController()
	if nc == nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "当前设备不支持网络控制"})
		return
	}
	if s.pool.IsVoWiFiActive(deviceID) && !device.PhoneModeCampsOnCell(worker.Config.PhoneMode) {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "message": "VoWiFi 运行中，无法启动数据网络"})
		return
	}
	if err := worker.StartNetwork(); err != nil {
		if writeLebaraUKRFLockError(c, err) {
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "启动数据网络失败: " + err.Error()})
		return
	}
	go s.refreshManagedWorkerRuntime(worker, "start_network")
	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"message":           "数据网络已启动",
		"device":            deviceID,
		"network_connected": worker.NetworkConnected(),
		"private_ip":        nc.GetPrivateIP(),
		"private_ipv6":      nc.GetPrivateIPv6(),
		"public_ip":         worker.GetCachedIP(),
		"public_ipv6":       worker.GetCachedIPv6(),
	})
}

func (s *Server) useDeviceMgmtStopNetwork(c businessContext) {
	deviceID := deviceIDParam(c)
	if deviceID == "" || s == nil || s.pool == nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "设备参数无效"})
		return
	}
	releaseMutation := s.pool.AcquireManagedDeviceMutation(deviceID)
	defer releaseMutation()
	s.useDeviceMgmtStopNetworkLocked(c, deviceID)
}

func (s *Server) useDeviceMgmtStopNetworkLocked(c businessContext, deviceID string) {
	worker := s.pool.GetWorker(deviceID)
	if worker == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备未找到"})
		return
	}
	nc := worker.NetworkController()
	if nc == nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "当前设备不支持网络控制"})
		return
	}
	if err := worker.StopNetwork(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "停止数据网络失败: " + err.Error()})
		return
	}
	go s.refreshManagedWorkerRuntime(worker, "stop_network")
	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"message":           "数据网络已停止",
		"device":            deviceID,
		"network_connected": worker.NetworkConnected(),
		"private_ip":        "",
		"private_ipv6":      "",
		"public_ip":         "",
		"public_ipv6":       "",
	})
}

func (s *Server) useVoWiFiEnableLocked(c businessContext, deviceID string) {
	if deviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "请指定设备 ID"})
		return
	}

	if s.pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "服务未就绪"})
		return
	}

	if err := s.pool.EnableVoWiFi(deviceID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "VoWiFi 启用失败: " + err.Error(),
			"device":  deviceID,
		})
		return
	}

	message := "VoWiFi 已启用，设备已进入飞行模式"
	if worker := s.pool.GetWorker(deviceID); worker != nil && worker.Config.PhoneMode == "cellular" {
		if worker.Config.NetworkEnabled || worker.Config.DataStrategy == "always" {
			message = "蜂窝数据通话已启用"
		} else {
			message = "蜂窝模式已设置，会正常驻网。打开「网络」后才会走流量"
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": message,
		"device":  deviceID,
	})
}

func (s *Server) useVoWiFiDisableLocked(c businessContext, deviceID string) {
	if s.pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "服务未就绪"})
		return
	}

	if err := s.pool.DisableVoWiFi(deviceID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "VoWiFi 禁用失败: " + err.Error(),
			"device":  deviceID,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "VoWiFi 已禁用",
		"device":  deviceID,
	})
}
