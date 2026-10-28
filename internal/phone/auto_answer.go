package phone

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost/voicehost"
	"github.com/yibaiba/hideck/pkg/logger"
)

const autoAnswerOwner = "hideck-auto-answer"

type autoAnswerRuntime struct {
	audioPath  string
	pcm        []int16
	sampleRate int
	devices    map[string]AutoAnswerDeviceConfig
}

type autoAnswerPlayback struct {
	delay      time.Duration
	pcm        []int16
	sampleRate int
}

func (s *Service) ConfigureAutoAnswer(config AutoAnswerConfig) error {
	return s.configureAutoAnswer(config)
}

func (s *Service) AutoAnswerStatus() AutoAnswerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return AutoAnswerStatus{
		AudioPath: s.autoAnswer.audioPath, Devices: cloneAutoAnswerDevices(s.autoAnswer.devices),
	}
}

func (s *Service) ConfigureAutoAnswerDevice(deviceID string, config AutoAnswerDeviceConfig) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("phone: auto-answer device ID is required")
	}
	if config.Enabled && config.Delay <= 0 {
		return errors.New("phone: auto-answer delay must be positive")
	}
	s.mu.RLock()
	audioPath := s.autoAnswer.audioPath
	s.mu.RUnlock()
	var pcm []int16
	var sampleRate int
	var err error
	if config.Enabled {
		pcm, sampleRate, err = s.decodeAutoAnswerAudio(audioPath)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.autoAnswer.audioPath != audioPath {
		return errors.New("phone: auto-answer audio changed while applying device settings")
	}
	if s.autoAnswer.devices == nil {
		s.autoAnswer.devices = make(map[string]AutoAnswerDeviceConfig)
	}
	s.autoAnswer.devices[deviceID] = config
	if config.Enabled {
		s.autoAnswer.pcm, s.autoAnswer.sampleRate = append([]int16(nil), pcm...), sampleRate
	}
	s.rescheduleAutoAnswerDeviceLocked(deviceID, config)
	return nil
}

func (s *Service) RemoveAutoAnswerDevice(deviceID string) {
	deviceID = strings.TrimSpace(deviceID)
	s.mu.Lock()
	delete(s.autoAnswer.devices, deviceID)
	for _, call := range s.calls {
		if call.view.DeviceID == deviceID && call.autoAnswerTimer != nil {
			call.autoAnswerTimer.Stop()
			call.autoAnswerTimer = nil
		}
	}
	s.mu.Unlock()
}

func (s *Service) ConfigureAutoAnswerAudio(path string) error {
	path = strings.TrimSpace(path)
	pcm, sampleRate, err := s.decodeAutoAnswerAudio(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.autoAnswer.audioPath = path
	s.autoAnswer.pcm, s.autoAnswer.sampleRate = append([]int16(nil), pcm...), sampleRate
	s.mu.Unlock()
	return nil
}

func (s *Service) rescheduleAutoAnswerDeviceLocked(deviceID string, config AutoAnswerDeviceConfig) {
	for _, call := range s.calls {
		if call.view.DeviceID != deviceID {
			continue
		}
		if call.autoAnswerTimer != nil {
			call.autoAnswerTimer.Stop()
			call.autoAnswerTimer = nil
		}
		if config.Enabled && call.view.Direction == "inbound" && call.view.Status == StatusRinging && !call.terminal {
			s.scheduleAutoAnswerLocked(call, config.Delay)
		}
	}
}

func (s *Service) ValidateAutoAnswerAudio(path string) error {
	_, _, err := s.decodeAutoAnswerAudio(strings.TrimSpace(path))
	return err
}

// autoAnswerMaxDuration 是自动应答提示音的最长时长，超出部分在转换时截断。
const autoAnswerMaxDuration = 30 * time.Second

// audioPromptConverter 是提示音解码器可选实现的能力：把任意受支持的音频格式
// 转成标准单声道 PCM16 WAV，并按需要截断时长。
type audioPromptConverter interface {
	ToPCM16WAVWithin(ctx context.Context, inputPath, outputPath string, maxDuration time.Duration) error
}

// ConvertAutoAnswerAudio 把上传的音频解码并统一写成 WAV。
//
// 自动应答的播放链路只接受单声道 PCM16 WAV（8 kHz 或 16 kHz），这里顺带混单声道、
// 归一采样率并截断到 30 秒，运行时就不必关心来源格式。
func (s *Service) ConvertAutoAnswerAudio(sourcePath, targetPath string) error {
	converter, ok := s.promptDecoder.(audioPromptConverter)
	if !ok {
		return errors.New("phone: 当前音频解码器不支持格式转换")
	}
	return converter.ToPCM16WAVWithin(
		s.ctx, strings.TrimSpace(sourcePath), strings.TrimSpace(targetPath), autoAnswerMaxDuration,
	)
}

func (s *Service) configureAutoAnswer(config AutoAnswerConfig) error {
	runtime, err := s.loadAutoAnswer(config)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.autoAnswer = runtime
	for _, call := range s.calls {
		if call.autoAnswerTimer != nil {
			call.autoAnswerTimer.Stop()
			call.autoAnswerTimer = nil
		}
		deviceConfig, enabled := runtime.devices[call.view.DeviceID]
		if enabled && deviceConfig.Enabled && call.view.Direction == "inbound" && call.view.Status == StatusRinging && !call.terminal {
			s.scheduleAutoAnswerLocked(call, deviceConfig.Delay)
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) loadAutoAnswer(config AutoAnswerConfig) (autoAnswerRuntime, error) {
	runtime := autoAnswerRuntime{
		audioPath: strings.TrimSpace(config.AudioPath), devices: cloneAutoAnswerDevices(config.Devices),
	}
	if err := validateAutoAnswerDevices(runtime.devices); err != nil {
		return autoAnswerRuntime{}, err
	}
	if !hasEnabledAutoAnswerDevice(runtime.devices) {
		return runtime, nil
	}
	if runtime.audioPath == "" {
		return autoAnswerRuntime{}, errors.New("phone: auto-answer audio file is required")
	}
	pcm, sampleRate, err := s.decodeAutoAnswerAudio(runtime.audioPath)
	if err != nil {
		return autoAnswerRuntime{}, err
	}
	runtime.pcm, runtime.sampleRate = append([]int16(nil), pcm...), sampleRate
	return runtime, nil
}

func validateAutoAnswerDevices(devices map[string]AutoAnswerDeviceConfig) error {
	for deviceID, deviceConfig := range devices {
		if strings.TrimSpace(deviceID) == "" {
			return errors.New("phone: auto-answer device ID is required")
		}
		if deviceConfig.Enabled && deviceConfig.Delay <= 0 {
			return fmt.Errorf("phone: auto-answer delay for device %s must be positive", deviceID)
		}
	}
	return nil
}

func hasEnabledAutoAnswerDevice(devices map[string]AutoAnswerDeviceConfig) bool {
	for _, deviceConfig := range devices {
		if deviceConfig.Enabled {
			return true
		}
	}
	return false
}

func cloneAutoAnswerDevices(devices map[string]AutoAnswerDeviceConfig) map[string]AutoAnswerDeviceConfig {
	result := make(map[string]AutoAnswerDeviceConfig, len(devices))
	for deviceID, deviceConfig := range devices {
		result[deviceID] = deviceConfig
	}
	return result
}

func (s *Service) decodeAutoAnswerAudio(path string) ([]int16, int, error) {
	if s.promptDecoder == nil {
		return nil, 0, errors.New("phone: auto-answer audio decoder is unavailable")
	}
	pcm, sampleRate, err := s.promptDecoder.DecodePCM(s.ctx, path)
	if err != nil {
		return nil, 0, fmt.Errorf("phone: decode auto-answer audio: %w", err)
	}
	if len(pcm) == 0 || (sampleRate != 8000 && sampleRate != 16000) {
		return nil, 0, errors.New("phone: auto-answer WAV must contain mono PCM16 audio at 8 or 16 kHz")
	}
	return pcm, sampleRate, nil
}

func (s *Service) scheduleAutoAnswer(call *activeCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if call == nil || call.terminal || call.view.Status != StatusRinging {
		return
	}
	deviceConfig, ok := s.autoAnswer.devices[call.view.DeviceID]
	if !ok || !deviceConfig.Enabled {
		return
	}
	s.scheduleAutoAnswerLocked(call, deviceConfig.Delay)
}

func (s *Service) scheduleAutoAnswerLocked(call *activeCall, delay time.Duration) {
	if call.autoAnswerTimer != nil {
		call.autoAnswerTimer.Stop()
	}
	callID := call.view.CallID
	call.autoAnswerTimer = time.AfterFunc(delay, func() { s.autoAnswerCall(callID) })
}

func (s *Service) autoAnswerCall(callID string) {
	call, config, ok := s.claimAutoAnswer(callID)
	if !ok {
		return
	}
	media, err := s.media.CreateHeadless(autoAnswerOwner)
	if err == nil {
		err = media.Attach(call.incomingSDP)
	}
	if err == nil {
		err = s.answerWithHeadlessMedia(call, media)
	}
	if err != nil {
		if media != nil {
			s.media.Remove(media.ID)
		}
		s.releaseAutoAnswerClaim(call)
		logger.Error("未接来电自动接听失败", "device_id", call.view.DeviceID, "call_id", callID, "err", err)
		return
	}
	s.startMixedRecording(call, media)
	media.StartPrompt(config.pcm, config.sampleRate, func(playErr error) {
		logger.Error("自动接听录音播放失败", "device_id", call.view.DeviceID, "call_id", callID, "err", playErr)
		s.failMediaAttachment(callID, playErr)
	})
	logger.Info("未接来电已自动接听", "device_id", call.view.DeviceID, "call_id", callID, "delay", config.delay)
}

func (s *Service) claimAutoAnswer(callID string) (*activeCall, autoAnswerPlayback, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[callID]
	if call == nil || call.terminal || call.view.Status != StatusRinging || call.controlPending {
		return nil, autoAnswerPlayback{}, false
	}
	deviceConfig, ok := s.autoAnswer.devices[call.view.DeviceID]
	if !ok || !deviceConfig.Enabled {
		return nil, autoAnswerPlayback{}, false
	}
	call.controlPending = true
	call.autoAnswerTimer = nil
	playback := autoAnswerPlayback{
		delay: deviceConfig.Delay, pcm: append([]int16(nil), s.autoAnswer.pcm...), sampleRate: s.autoAnswer.sampleRate,
	}
	return call, playback, true
}

func (s *Service) answerWithHeadlessMedia(call *activeCall, media *MediaSession) error {
	s.mu.RLock()
	deviceID, callID, incomingSDP := call.view.DeviceID, call.view.CallID, call.incomingSDP
	s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	answer, err := s.gateway.AnswerIncomingCall(ctx, voicehost.AnswerRequest{
		DeviceID: deviceID, CallID: callID, SDP: media.PlainSDP(),
	})
	if err != nil {
		return err
	}
	if answer.OfferSDP != "" && answer.OfferSDP != incomingSDP {
		if err := media.Attach(answer.OfferSDP); err != nil {
			hangupErr := s.gateway.HangupCall(ctx, deviceID, callID)
			return errors.Join(err, hangupErr)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls[callID] != call || call.terminal {
		go s.hangupAutoAnsweredCall(deviceID, callID)
		return errors.New("phone: incoming call ended during auto-answer")
	}
	call.controlPending, call.autoAnswered = false, true
	call.owner, call.mediaID = autoAnswerOwner, media.ID
	s.mediaCalls[media.ID] = callID
	return nil
}

func (s *Service) hangupAutoAnsweredCall(deviceID, callID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.gateway.HangupCall(ctx, deviceID, callID); err != nil {
		logger.Error("结束失去归属的自动接听通话失败", "device_id", deviceID, "call_id", callID, "err", err)
	}
}

func (s *Service) releaseAutoAnswerClaim(call *activeCall) {
	s.mu.Lock()
	if current := s.calls[call.view.CallID]; current == call && !call.terminal {
		call.controlPending = false
	}
	s.mu.Unlock()
}
