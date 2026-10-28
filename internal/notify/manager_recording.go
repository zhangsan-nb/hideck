package notify

import (
	"strings"

	"github.com/yibaiba/hideck/internal/phone"
)

// NotifyAutoAnswerRecording publishes the recording produced by a headless
// automatic answer. Channels without media support still receive the text.
func (m *Manager) NotifyAutoAnswerRecording(notification phone.AutoAnswerRecordingNotification) {
	if m == nil || m.channelCount() == 0 {
		return
	}
	deviceID := strings.TrimSpace(notification.DeviceID)
	peer := strings.TrimSpace(notification.Peer)
	recordingPath := strings.TrimSpace(notification.RecordingPath)
	recordingName := strings.TrimSpace(notification.RecordingName)
	if recordingPath == "" || recordingName == "" {
		return
	}
	region := m.phoneNumberRegion(deviceID)
	fields := []string{"自动接听录音", "设备    " + m.deviceLabel(deviceID)}
	fields = append(fields, phoneIdentityFieldsWithRegion("来电", peer, region)...)
	fields = append(fields, "录音    "+recordingName)
	ctx := NotificationContext{
		Event: "call_recording", Title: "自动接听录音", Text: strings.Join(fields, "\n"),
		DeviceID: deviceID, DeviceName: m.resolveDeviceName(deviceID), Number: peer, Timestamp: notification.At,
	}
	fillPhoneIdentityWithRegion(&ctx, peer, region)
	m.broadcastWithAttachments(ctx, []CommandAttachment{{
		Type: "audio", Recording: recordingName, ContentType: "audio/mpeg",
		Path: recordingPath, Codec: "MP3",
	}})
}
