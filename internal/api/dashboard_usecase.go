package api

import (
	"net/http"

	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/device"
	"github.com/yibaiba/hideck/internal/volte"

	"github.com/gin-gonic/gin"
)

func (s *Server) useListDevices(c businessContext) {
	workers := s.pool.GetAllWorkers()
	cfgByID := map[string]config.DeviceConfig{}
	{
		managed := config.ListDevices()
		for _, d := range managed {
			cfgByID[d.ID] = d
		}
	}

	type DeviceStatus struct {
		ID               string                            `json:"id"`
		Name             string                            `json:"name"`
		Interface        string                            `json:"interface"`
		ProxyPort        int                               `json:"proxy_port"`
		PublicIP         string                            `json:"public_ip"`
		PublicIPv6       string                            `json:"public_ipv6,omitempty"`
		Healthy          bool                              `json:"healthy"`
		Operator         string                            `json:"operator"`
		SignalDBM        int                               `json:"signal_dbm"`
		NetworkMode      string                            `json:"network_mode"`
		NetworkDuplex    string                            `json:"network_duplex"`
		PhoneMode        string                            `json:"phone_mode,omitempty"`
		VoWiFiActive     bool                              `json:"vowifi_active"`
		VoWiFiRuntime    *voWiFiRuntimeDTO                 `json:"vowifi_runtime,omitempty"`
		VoWiFiHealth     *device.WiFiCallingHealthSnapshot `json:"vowifi_health,omitempty"`
		NativeVoLTE      *volte.Status                     `json:"native_volte,omitempty"`
		ModemVoice       map[string]interface{}            `json:"modem_voice,omitempty"`
		Traffic          map[string]string                 `json:"traffic,omitempty"`
		NetworkConnected bool                              `json:"network_connected"`
	}

	list := make([]DeviceStatus, 0, len(workers))
	for _, w := range workers {
		status := w.GetCachedDeviceStatus() // 仓表盘列表读缓存，0 IPC
		cfg := w.Config
		if v, ok := cfgByID[w.ID]; ok {
			// PhoneMode 等策略字段只在 worker 运行时投影，yaml 里是空的。
			// 直接用 persisted 会把联通原生 VoLTE 画成 VoWiFi。
			cfg = overviewDisplayConfig(w.Config, v, true)
		}
		item := DeviceStatus{
			ID:               cfg.ID,
			Name:             cfg.Name,
			Interface:        cfg.Interface,
			ProxyPort:        cfg.ProxyPort,
			PublicIP:         w.GetCachedIP(),
			PublicIPv6:       w.GetCachedIPv6(),
			Healthy:          w.GetCachedHealthy(), // 健康状态读缓存
			Operator:         device.DisplayOperatorName(status, s.pool.IsVoWiFiActive(w.ID)),
			SignalDBM:        status.SignalDBM,
			NetworkMode:      status.NetworkMode,
			NetworkDuplex:    status.NetworkDuplex,
			PhoneMode:        overviewPhoneMode(cfg.PhoneMode),
			VoWiFiActive:     s.pool.IsVoWiFiActive(w.ID), // 逐个设备判断 VoWiFi 状态，支持多设备
			VoWiFiRuntime:    s.getVoWiFiRuntimeDTO(w.ID),
			VoWiFiHealth:     s.getWiFiCallingHealth(w.ID),
			ModemVoice:       s.pool.ModemVoiceStatus(w.ID),
			NetworkConnected: w.NetworkConnected(),
		}
		if device.IsNativeVoLTEMode(cfg.PhoneMode) {
			volteStatus := s.pool.NativeVoLTEStatus(w.ID)
			item.NativeVoLTE = &volteStatus
		}
		// 添加格式化流量
		if w.Proxy != nil {
			item.Traffic = w.Proxy.GetFormattedStats()
		}
		list = append(list, item)
	}
	c.JSON(http.StatusOK, list)
}

func (s *Server) useStatusDetail(c businessContext) {
	deviceID := deviceIDParam(c)
	worker := s.pool.GetWorker(deviceID)
	if worker == nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备未找到"})
		return
	}

	_ = worker.RefreshRuntime(businessRequestContext(c), "status_detail")
	_ = worker.RefreshIdentityLive(businessRequestContext(c), "status_detail")
	status := worker.ProjectDeviceStatus()

	response := gin.H{
		"id":                worker.ID,
		"name":              worker.Config.Name,
		"imei":              status.IMEI,
		"firmware":          status.Firmware,
		"iccid":             status.ICCID,
		"imsi":              status.IMSI,
		"native_spn":        status.NativeSPN,
		"native_mcc":        status.NativeMCC,
		"native_mnc":        status.NativeMNC,
		"gid1":              status.GID1,
		"gid2":              status.GID2,
		"pnn":               status.PNN,
		"opl":               status.OPL,
		"sim_service_table": status.SIMServiceTable,
		"operator":          status.Operator,
		"sim_inserted":      status.SimInserted,
		"signal_dbm":        status.SignalDBM,
		"signal_rsrp":       status.SignalRSRP,
		"signal_rsrq":       status.SignalRSRQ,
		"signal_sinr":       status.SignalSINR,
		"nr5g_signal_sinr":  status.NR5GSignalSINR,
		"radio_band":        status.RadioBand,
		"radio_channel":     status.RadioChannel,
		"reg_status":        status.RegStatus,
		"reg_status_text":   status.RegStatusText,
		"lac":               status.LAC,
		"cell_id":           status.CellID,
		"apn":               status.APN,
		"ims_status":        status.IMSStatus,
		"public_ip":         worker.GetCachedIP(),
		"public_ipv6":       worker.GetCachedIPv6(),
		"interface":         worker.Config.Interface,
		"proxy_port":        worker.Config.ProxyPort,
		"healthy":           worker.IsDeviceHealthy(),
		"network_connected": worker.NetworkConnected(),
	}

	if worker.Proxy != nil {
		response["traffic"] = worker.Proxy.GetFormattedStats()
	}

	vowifi := gin.H{
		"active": s.pool.IsVoWiFiActive(worker.ID),
	}
	if obs := s.pool.GetVoWiFiObs(worker.ID); obs != nil {
		for k, v := range obs {
			vowifi[k] = v
		}
	} else {
		if app := s.pool.GetVoWiFiAppForDevice(worker.ID); app != nil {
			status := app.Status()
			vowifi["imscore"] = status
			vowifi["smsip"] = status
		}
	}
	if s.voiceGW != nil {
		vowifi["voice"] = s.voiceGW.DeviceStatus(worker.ID)
	}
	response["vowifi"] = vowifi

	c.JSON(http.StatusOK, response)
}

func (s *Server) useHealth(c businessContext) {
	workers := s.pool.GetAllWorkers()
	allHealthy := true

	type DeviceHealth struct {
		Healthy          bool `json:"healthy"`
		ModemOK          bool `json:"modem_ok"`
		IfaceUp          bool `json:"iface_up"`
		NetworkConnected bool `json:"network_connected"`
		Signal           int  `json:"signal,omitempty"`
	}

	status := make(map[string]DeviceHealth)
	for _, w := range workers {
		modemOK := w.IsDeviceHealthy()
		ifaceUp := false
		healthy := modemOK

		if w.QMICore != nil {
			ifaceUp = w.QMICore.IsInterfaceUp()
		}

		// 获取信号 (非阻塞)
		signal := 0
		stats := w.GetStats()
		if s, ok := stats["signal"].(int); ok {
			signal = s
		}

		status[w.ID] = DeviceHealth{
			Healthy:          healthy,
			ModemOK:          modemOK,
			IfaceUp:          ifaceUp,
			NetworkConnected: ifaceUp,
			Signal:           signal,
		}
		if !healthy {
			allHealthy = false
		}
	}

	if allHealthy {
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "devices": status})
	} else {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "devices": status})
	}
}

func (s *Server) useDeviceRescan(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	if s.pool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "服务未就绪"})
		return
	}

	if err := s.pool.RescanAndReconnect(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "重新扫描失败: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "设备重新扫描完成",
	})
}
