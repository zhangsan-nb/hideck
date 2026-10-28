package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/phone"
)

type settingsPromptDecoder struct{}

func (settingsPromptDecoder) DecodePCM(_ context.Context, path string) ([]int16, int, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, 0, err
	}
	return make([]int16, 160), 8000, nil
}

func (settingsPromptDecoder) ToPCM16WAVWithin(_ context.Context, inputPath, outputPath string, _ time.Duration) error {
	payload, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, payload, 0o600)
}

func TestPhoneSettingsPayloadUsesEightSecondDefault(t *testing.T) {
	service, err := phone.NewService(phone.ServiceOptions{
		Gateway: &phoneRouteGatewayStub{}, WebRTCUDPAddress: "127.0.0.1:0",
		AutoAnswer: phone.AutoAnswerConfig{Devices: map[string]phone.AutoAnswerDeviceConfig{
			"dev-1": {Enabled: false},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })

	payload := (&Server{phone: service}).phoneSettingsPayload()
	if len(payload.AutoAnswer.Devices) != 1 || payload.AutoAnswer.Devices[0].DelaySeconds != 8 {
		t.Fatalf("auto answer payload = %+v, want 8 second default", payload.AutoAnswer.Devices)
	}
}

func TestPhoneSettingsEnableAutoAnswerAndPersist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "greeting.wav")
	if err := os.WriteFile(audioPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	fixture := []byte("system:\n  auto_answer:\n    audio_file: " + audioPath + "\n    devices:\n      dev-2:\n        enabled: false\n        delay_seconds: 20\ndevices: []\n")
	if err := os.WriteFile(configPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := phone.NewService(phone.ServiceOptions{
		Gateway: &phoneRouteGatewayStub{}, WebRTCUDPAddress: "127.0.0.1:0", PromptDecoder: settingsPromptDecoder{},
		AutoAnswer: phone.AutoAnswerConfig{AudioPath: audioPath, Devices: map[string]phone.AutoAnswerDeviceConfig{
			"dev-2": {Delay: 20 * time.Second},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Close(ctx)
	})
	server := &Server{
		phone: service, configPath: configPath,
		fullCfg: &config.Config{
			Devices: []config.DeviceConfig{{ID: "dev-1"}, {ID: "dev-2"}},
			System: config.SystemConfig{AutoAnswer: config.AutoAnswerConfig{
				AudioFile: audioPath,
				Devices:   map[string]config.AutoAnswerDeviceConfig{"dev-2": {DelaySeconds: 20}},
			}},
		},
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/settings/phone", bytes.NewBufferString(
		`{"enabled":true,"delay_seconds":45}`,
	))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "device_id", Value: "dev-1"}}
	server.handleUpdateAutoAnswerDevice(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	status := service.AutoAnswerStatus().Devices["dev-1"]
	if !status.Enabled || status.Delay != 45*time.Second {
		t.Fatalf("runtime status = %+v", status)
	}
	if other := service.AutoAnswerStatus().Devices["dev-2"]; other.Enabled || other.Delay != 20*time.Second {
		t.Fatalf("other device runtime status changed = %+v", other)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	deviceConfig := loaded.System.AutoAnswer.Devices["dev-1"]
	if !deviceConfig.Enabled || deviceConfig.DelaySeconds != 45 || loaded.System.AutoAnswer.AudioFile != audioPath {
		t.Fatalf("persisted auto answer = %+v", loaded.System.AutoAnswer)
	}
	if other := loaded.System.AutoAnswer.Devices["dev-2"]; other.Enabled || other.DelaySeconds != 20 {
		t.Fatalf("other device persisted settings changed = %+v", other)
	}
}

func TestPhoneSettingsCannotEnableWithoutReadableAudio(t *testing.T) {
	service, err := phone.NewService(phone.ServiceOptions{
		Gateway: &phoneRouteGatewayStub{}, WebRTCUDPAddress: "127.0.0.1:0", PromptDecoder: settingsPromptDecoder{},
		AutoAnswer: phone.AutoAnswerConfig{AudioPath: filepath.Join(t.TempDir(), "missing.wav")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	server := &Server{phone: service, configPath: filepath.Join(t.TempDir(), "config.yaml"), fullCfg: &config.Config{
		Devices: []config.DeviceConfig{{ID: "dev-1"}},
	}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/settings/phone", bytes.NewBufferString(
		`{"enabled":true,"delay_seconds":30}`,
	))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "device_id", Value: "dev-1"}}
	server.handleUpdateAutoAnswerDevice(ctx)

	if recorder.Code != http.StatusUnprocessableEntity || service.AutoAnswerStatus().Devices["dev-1"].Enabled {
		t.Fatalf("status=%d body=%s runtime=%+v", recorder.Code, recorder.Body.String(), service.AutoAnswerStatus())
	}
}

func TestPhoneSettingsUploadsAndActivatesAudio(t *testing.T) {
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "auto-answer.wav")
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte("devices: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := phone.NewService(phone.ServiceOptions{
		Gateway: &phoneRouteGatewayStub{}, WebRTCUDPAddress: "127.0.0.1:0", PromptDecoder: settingsPromptDecoder{},
		AutoAnswer: phone.AutoAnswerConfig{AudioPath: audioPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	server := &Server{
		phone: service, configPath: configPath,
		fullCfg: &config.Config{System: config.SystemConfig{AutoAnswer: config.AutoAnswerConfig{AudioFile: audioPath}}},
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "greeting.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fixture wav")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/settings/phone/auto-answer/audio", body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	server.handleUploadAutoAnswerAudio(ctx)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	content, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "fixture wav" {
		t.Fatalf("uploaded audio = %q", content)
	}
}

func TestPhoneSettingsUploadRestoresPreviousAudioWhenConfigWriteFails(t *testing.T) {
	directory := t.TempDir()
	audioPath := filepath.Join(directory, "auto-answer.wav")
	if err := os.WriteFile(audioPath, []byte("previous wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := phone.NewService(phone.ServiceOptions{
		Gateway: &phoneRouteGatewayStub{}, WebRTCUDPAddress: "127.0.0.1:0", PromptDecoder: settingsPromptDecoder{},
		AutoAnswer: phone.AutoAnswerConfig{AudioPath: audioPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	server := &Server{
		phone: service, configPath: directory,
		fullCfg: &config.Config{System: config.SystemConfig{AutoAnswer: config.AutoAnswerConfig{AudioFile: audioPath}}},
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "replacement.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("replacement wav")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/settings/phone/auto-answer/audio", body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	server.handleUploadAutoAnswerAudio(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	content, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "previous wav" {
		t.Fatalf("restored audio = %q", content)
	}
}
