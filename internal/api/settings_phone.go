package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/phone"
	"github.com/yibaiba/hideck/pkg/logger"
)

const (
	maxAutoAnswerAudioBytes       = 16 << 20
	defaultAutoAnswerDelaySeconds = 8
)

type phoneSettingsResponse struct {
	AutoAnswer autoAnswerSettingsResponse `json:"auto_answer"`
}

type autoAnswerSettingsResponse struct {
	AudioReady bool                               `json:"audio_ready"`
	AudioName  string                             `json:"audio_name"`
	Devices    []autoAnswerDeviceSettingsResponse `json:"devices"`
}

type autoAnswerDeviceSettingsResponse struct {
	DeviceID     string `json:"device_id"`
	Enabled      bool   `json:"enabled"`
	DelaySeconds int    `json:"delay_seconds"`
}

type updateAutoAnswerDeviceRequest struct {
	Enabled      bool `json:"enabled"`
	DelaySeconds int  `json:"delay_seconds"`
}

func (s *Server) handleGetPhoneSettings(c *gin.Context) {
	s.useGetPhoneSettings(c)
}

func (s *Server) useGetPhoneSettings(c businessContext) {
	if !s.requirePhone(c) {
		return
	}
	c.JSON(http.StatusOK, s.phoneSettingsPayload())
}

func (s *Server) handleUpdateAutoAnswerDevice(c *gin.Context) {
	s.useUpdateAutoAnswerDevice(c)
}

func (s *Server) useUpdateAutoAnswerDevice(c businessContext) {
	releaseOperation, allowed := s.acquireBusinessMutation(c)
	if !allowed {
		return
	}
	defer releaseOperation()

	if !s.requirePhone(c) {
		return
	}
	deviceID := strings.TrimSpace(c.Param("device_id"))
	if deviceID == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备不存在"})
		return
	}
	var request updateAutoAnswerDeviceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "参数错误"})
		return
	}
	if request.DelaySeconds <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "自动接听等待时间必须大于 0 秒"})
		return
	}
	delay := time.Duration(request.DelaySeconds) * time.Second
	if int(delay/time.Second) != request.DelaySeconds || delay <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "自动接听等待时间超出支持范围"})
		return
	}
	s.phoneSettingsMu.Lock()
	defer s.phoneSettingsMu.Unlock()
	if !s.hasAutoAnswerDevice(deviceID) {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "message": "设备不存在"})
		return
	}
	status := s.phone.AutoAnswerStatus()
	previous, existed := status.Devices[deviceID]
	next := phone.AutoAnswerDeviceConfig{
		Enabled: request.Enabled, Delay: delay,
	}
	if err := s.phone.ConfigureAutoAnswerDevice(deviceID, next); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "message": err.Error()})
		return
	}
	persisted := config.AutoAnswerDeviceConfig{
		Enabled: next.Enabled, DelaySeconds: request.DelaySeconds,
	}
	if err := config.UpdateAutoAnswerDeviceInFile(s.configPath, deviceID, persisted); err != nil {
		rollbackErr := s.rollbackAutoAnswerDevice(deviceID, previous, existed)
		logger.Error("写入自动接听配置失败", "err", err, "rollback_err", rollbackErr)
		message := "写入配置文件失败: " + err.Error()
		if rollbackErr != nil {
			message += "; 运行时回滚失败: " + rollbackErr.Error()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": message})
		return
	}
	if s.fullCfg != nil {
		s.systemConfigMu.Lock()
		if s.fullCfg.System.AutoAnswer.Devices == nil {
			s.fullCfg.System.AutoAnswer.Devices = make(map[string]config.AutoAnswerDeviceConfig)
		}
		s.fullCfg.System.AutoAnswer.Devices[deviceID] = persisted
		s.systemConfigMu.Unlock()
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "applied": true})
}

func (s *Server) rollbackAutoAnswerDevice(deviceID string, previous phone.AutoAnswerDeviceConfig, existed bool) error {
	if !existed {
		s.phone.RemoveAutoAnswerDevice(deviceID)
		return nil
	}
	return s.phone.ConfigureAutoAnswerDevice(deviceID, previous)
}

func (s *Server) hasAutoAnswerDevice(deviceID string) bool {
	if s.pool != nil {
		return s.pool.GetWorker(deviceID) != nil
	}
	if s.fullCfg != nil {
		for _, deviceConfig := range s.fullCfg.Devices {
			if strings.TrimSpace(deviceConfig.ID) == deviceID {
				return true
			}
		}
	}
	return false
}

func (s *Server) phoneSettingsPayload() phoneSettingsResponse {
	status := s.phone.AutoAnswerStatus()
	devices := make([]autoAnswerDeviceSettingsResponse, 0, len(status.Devices))
	for deviceID, deviceConfig := range status.Devices {
		delaySeconds := int(deviceConfig.Delay / time.Second)
		if delaySeconds <= 0 {
			delaySeconds = defaultAutoAnswerDelaySeconds
		}
		devices = append(devices, autoAnswerDeviceSettingsResponse{
			DeviceID: deviceID, Enabled: deviceConfig.Enabled,
			DelaySeconds: delaySeconds,
		})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceID < devices[j].DeviceID })
	audioName := ""
	if status.AudioPath != "" {
		audioName = filepath.Base(status.AudioPath)
	}
	return phoneSettingsResponse{AutoAnswer: autoAnswerSettingsResponse{
		AudioReady: status.AudioPath != "" && s.phone.ValidateAutoAnswerAudio(status.AudioPath) == nil,
		AudioName:  audioName, Devices: devices,
	}}
}

// autoAnswerAudioFormats 是允许上传的自动接听录音格式。除 WAV 外都需要在服务端
// 解码后统一转成单声道 PCM16 WAV 再落盘，运行时只认这一种格式。
var autoAnswerAudioFormats = map[string]struct{}{
	".wav": {}, ".amr": {}, ".amr-wb": {}, ".mp3": {},
}

func (s *Server) handleUploadAutoAnswerAudio(c *gin.Context) {
	if !s.requirePhone(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAutoAnswerAudioBytes+(1<<20))
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "请选择不超过 16 MiB 的音频文件"})
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	source, err := file.Open()
	if err != nil {
		s.respondAutoAnswerUploadError(c, err)
		return
	}
	defer source.Close()
	s.installAutoAnswerContent(c, file.Filename, source)
}

func (s *Server) installAutoAnswerContent(c businessContext, name string, source io.Reader) {
	extension := strings.ToLower(filepath.Ext(name))
	if _, ok := autoAnswerAudioFormats[extension]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "error", "message": "自动接听录音支持 WAV、AMR、AMR-WB 或 MP3 文件",
		})
		return
	}
	s.phoneSettingsMu.Lock()
	defer s.phoneSettingsMu.Unlock()
	target := s.autoAnswerAudioPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		s.respondAutoAnswerUploadError(c, err)
		return
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".auto-answer-*"+extension)
	if err != nil {
		s.respondAutoAnswerUploadError(c, err)
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := copyAutoAnswerAudio(source, temporary); err != nil {
		s.respondAutoAnswerUploadError(c, err)
		return
	}
	// 所有来源统一转换：混单声道、采样率归一到 8 kHz、超过 30 秒截断。走同一条
	// 路径也让 44.1 kHz 立体声 WAV 这类"能解码但不符合播放要求"的文件可用。
	convertedPath := temporaryPath + ".wav"
	if err := s.phone.ConvertAutoAnswerAudio(temporaryPath, convertedPath); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"status": "error", "message": "音频格式转换失败：" + err.Error(),
		})
		return
	}
	defer os.Remove(convertedPath)
	installSource := convertedPath
	if err := s.phone.ValidateAutoAnswerAudio(installSource); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "message": err.Error()})
		return
	}
	if err := os.Chmod(installSource, 0o600); err != nil {
		s.respondAutoAnswerUploadError(c, err)
		return
	}
	if err := s.installAutoAnswerAudio(installSource, target); err != nil {
		s.respondAutoAnswerUploadError(c, err)
		return
	}
	c.JSON(http.StatusCreated, s.phoneSettingsPayload().AutoAnswer)
}

func copyAutoAnswerAudio(source io.Reader, target *os.File) error {
	_, copyErr := io.Copy(target, io.LimitReader(source, maxAutoAnswerAudioBytes+1))
	closeErr := errors.Join(target.Close())
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(target.Name())
	if err != nil {
		return err
	}
	if info.Size() > maxAutoAnswerAudioBytes {
		return errors.New("自动接听录音不能超过 16 MiB")
	}
	return nil
}

func (s *Server) reloadAutoAnswerAudio(path string) error {
	if err := s.phone.ConfigureAutoAnswerAudio(path); err != nil {
		return err
	}
	if err := config.UpdateAutoAnswerAudioFileInFile(s.configPath, path); err != nil {
		return err
	}
	if s.fullCfg != nil {
		s.systemConfigMu.Lock()
		s.fullCfg.System.AutoAnswer.AudioFile = path
		s.systemConfigMu.Unlock()
	}
	return nil
}

func (s *Server) autoAnswerAudioPath() string {
	path := ""
	if s.fullCfg != nil {
		s.systemConfigMu.RLock()
		path = strings.TrimSpace(s.fullCfg.System.AutoAnswer.AudioFile)
		s.systemConfigMu.RUnlock()
	}
	if path == "" {
		return "data/phone/auto-answer.wav"
	}
	// 落盘内容统一是 WAV；旧配置若指向 .amr 之类后缀会按后缀解码失败，这里同步改掉。
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		path = strings.TrimSuffix(path, filepath.Ext(path)) + ".wav"
	}
	return path
}

func (s *Server) respondAutoAnswerUploadError(c businessContext, err error) {
	logger.Error("保存自动接听录音失败", "err", err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"status": "error", "message": fmt.Sprintf("保存自动接听录音失败: %v", err),
	})
}
