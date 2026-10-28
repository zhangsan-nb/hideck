package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/device"
	"github.com/yibaiba/hideck/pkg/logger"
)

type managedDeviceDeleteOps struct {
	abandon      func(string) error
	deleteConfig func(string) error
	detach       func(string) error
}

func stopManagedDevice(c businessContext, id string, abandon func(string) error) bool {
	if abandon == nil {
		return true
	}
	if err := abandon(id); err != nil {
		logger.Warn("停止设备失败，保留设备配置", "device", id, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error", "message": "停止设备失败，配置未修改；请检查设备日志后重试",
		})
		return false
	}
	return true
}

func deleteManagedDevice(c businessContext, configPath string, ops managedDeviceDeleteOps) {
	id := deviceIDParam(c)
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "必须填写 id"})
		return
	}
	if !stopManagedDevice(c, id, ops.abandon) {
		return
	}
	deleteConfig := ops.deleteConfig
	if deleteConfig == nil {
		deleteConfig = func(deviceID string) error { return config.DeleteDeviceInFile(configPath, deviceID) }
	}
	if err := deleteConfig(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "删除设备配置失败: " + err.Error()})
		return
	}
	if ops.detach != nil {
		if err := ops.detach(id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "error", "message": "设备配置已删除，但清理历史设备绑定失败: " + err.Error(),
			})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func databaseManagedDeviceDeleteOps(abandon func(string) error) managedDeviceDeleteOps {
	return managedDeviceDeleteOps{abandon: abandon, detach: db.DetachDeviceAlias}
}

func managedDeviceIMEI(deviceID string, pool *device.Pool) string {
	if pool != nil {
		if worker := pool.GetWorker(deviceID); worker != nil {
			if imei := strings.TrimSpace(worker.Config.ModemIMEI); config.NormalizeIMEI(imei) != "" {
				return imei
			}
		}
	}
	cfg, err := config.GetDeviceByID(deviceID)
	if err != nil || cfg == nil {
		return ""
	}
	if imei := strings.TrimSpace(cfg.ModemIMEI); config.NormalizeIMEI(imei) != "" {
		return imei
	}
	return ""
}

func exactManagedDeviceDetach(imei string) func(string) error {
	if imei = strings.TrimSpace(imei); config.NormalizeIMEI(imei) != "" {
		return func(string) error { return db.DetachDeviceIMEI(imei) }
	}
	return db.DetachDeviceAlias
}

func (s *Server) deleteManagedDeviceConfig(deviceID string) error {
	s.notificationConfigMu.Lock()
	defer s.notificationConfigMu.Unlock()
	s.phoneSettingsMu.Lock()
	defer s.phoneSettingsMu.Unlock()
	if err := config.DeleteDeviceInFile(s.configPath, deviceID); err != nil {
		return err
	}
	if s.fullCfg == nil {
		if s.phone != nil {
			s.phone.RemoveAutoAnswerDevice(deviceID)
		}
		return nil
	}
	filters := withoutNotificationDevice(s.fullCfg.NotificationDeviceFilters, deviceID)
	s.fullCfg.NotificationDeviceFilters = filters
	s.systemConfigMu.Lock()
	delete(s.fullCfg.System.AutoAnswer.Devices, deviceID)
	s.systemConfigMu.Unlock()
	if s.phone != nil {
		s.phone.RemoveAutoAnswerDevice(deviceID)
	}
	if s.notifyMgr != nil {
		s.notifyMgr.UpdateDeviceFilters(filters)
	}
	return nil
}

func withoutNotificationDevice(filters map[string][]string, deviceID string) map[string][]string {
	deviceID = strings.TrimSpace(deviceID)
	updated := make(map[string][]string)
	for channel, devices := range filters {
		removed := false
		for _, candidate := range devices {
			candidate = strings.TrimSpace(candidate)
			if candidate == deviceID {
				removed = true
				continue
			}
			if candidate != "" {
				updated[channel] = append(updated[channel], candidate)
			}
		}
		if removed && len(updated[channel]) == 0 {
			// Preserve a restrictive target. Removing the channel key would mean
			// "all devices" and could expose another device's SMS or call details.
			updated[channel] = []string{deviceID}
		}
	}
	return updated
}
