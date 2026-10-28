package audiotranscode

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildTestWAV(sampleRate, channels int, samples []int16) []byte {
	bytesPerFrame := 2 * channels
	dataSize := len(samples) * 2
	buffer := make([]byte, 44+dataSize)
	copy(buffer[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(36+dataSize))
	copy(buffer[8:12], "WAVE")
	copy(buffer[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buffer[16:20], 16)
	binary.LittleEndian.PutUint16(buffer[20:22], 1)
	binary.LittleEndian.PutUint16(buffer[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buffer[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buffer[28:32], uint32(sampleRate*bytesPerFrame))
	binary.LittleEndian.PutUint16(buffer[32:34], uint16(bytesPerFrame))
	binary.LittleEndian.PutUint16(buffer[34:36], 16)
	copy(buffer[36:40], "data")
	binary.LittleEndian.PutUint32(buffer[40:44], uint32(dataSize))
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(buffer[44+index*2:], uint16(sample))
	}
	return buffer
}

func TestDecodeWAVFlexibleDowmixesStereo(t *testing.T) {
	pcm, rate, err := decodeWAVFlexible(buildTestWAV(44100, 2, []int16{1000, 3000, -2000, -4000}))
	if err != nil {
		t.Fatalf("decodeWAVFlexible: %v", err)
	}
	if rate != 44100 {
		t.Fatalf("sample rate = %d, want 44100", rate)
	}
	want := []int16{2000, -3000}
	if len(pcm) != len(want) {
		t.Fatalf("samples = %v, want %v", pcm, want)
	}
	for index := range want {
		if pcm[index] != want[index] {
			t.Fatalf("samples = %v, want %v", pcm, want)
		}
	}
}

func TestToPCM16WAVNormalizesStereo44k(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "prompt.wav")
	// 1 秒 44.1 kHz 立体声，左右声道同值，便于校验混音后幅度不变。
	frames := 44100
	samples := make([]int16, frames*2)
	for index := range samples {
		samples[index] = 1000
	}
	if err := os.WriteFile(source, buildTestWAV(44100, 2, samples), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	target := filepath.Join(dir, "converted.wav")
	if err := New().ToPCM16WAV(context.Background(), source, target); err != nil {
		t.Fatalf("ToPCM16WAV: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	pcm, rate, err := decodePCM16WAV(data)
	if err != nil {
		t.Fatalf("decodePCM16WAV: %v", err)
	}
	if rate != 8000 {
		t.Fatalf("sample rate = %d, want 8000", rate)
	}
	if len(pcm) < 7990 || len(pcm) > 8010 {
		t.Fatalf("sample count = %d, want ≈8000", len(pcm))
	}
	if pcm[0] != 1000 {
		t.Fatalf("first sample = %d, want 1000", pcm[0])
	}
}

func TestEncodePCM16WAVRoundTripsThroughStrictParser(t *testing.T) {
	samples := []int16{0, 32767, -32768, 1234}
	payload, err := EncodePCM16WAV(samples, 16000)
	if err != nil {
		t.Fatalf("EncodePCM16WAV: %v", err)
	}
	pcm, rate, err := decodePCM16WAV(payload)
	if err != nil {
		t.Fatalf("decodePCM16WAV: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("sample rate = %d, want 16000", rate)
	}
	if len(pcm) != len(samples) {
		t.Fatalf("samples = %v, want %v", pcm, samples)
	}
	for index := range samples {
		if pcm[index] != samples[index] {
			t.Fatalf("samples = %v, want %v", pcm, samples)
		}
	}
}

// TestDecodeMP3Sample 用真实 MP3 验证解码链。样本路径由 HIDECK_MP3_SAMPLE 指定，
// 未设置时跳过——仓库不携带音频样本。
func TestDecodeMP3Sample(t *testing.T) {
	path := os.Getenv("HIDECK_MP3_SAMPLE")
	if path == "" {
		t.Skip("HIDECK_MP3_SAMPLE 未设置")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	_, _, err = decodeMP3(data)
	if err == nil {
		t.Fatal("expected low-sample-rate MP3 to be rejected with a hint")
	}
	if !strings.Contains(err.Error(), "44.1 kHz") {
		t.Fatalf("unexpected error message: %v", err)
	}
	t.Logf("rejected as expected: %v", err)
}

func TestToPCM16WAVTruncatesToThirtySeconds(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "long.wav")
	samples := make([]int16, 8000*40)
	for index := range samples {
		samples[index] = 500
	}
	if err := os.WriteFile(source, buildTestWAV(8000, 1, samples), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	target := filepath.Join(dir, "out.wav")
	if err := New().ToPCM16WAVWithin(context.Background(), source, target, 30*time.Second); err != nil {
		t.Fatalf("ToPCM16WAVWithin: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	pcm, rate, err := decodePCM16WAV(data)
	if err != nil {
		t.Fatalf("decodePCM16WAV: %v", err)
	}
	if rate != 8000 {
		t.Fatalf("sample rate = %d, want 8000", rate)
	}
	if len(pcm) != 8000*30 {
		t.Fatalf("sample count = %d, want %d", len(pcm), 8000*30)
	}
}

func TestPromptDecodersStopAtDurationLimit(t *testing.T) {
	t.Run("WAV allocation is limited before conversion", func(t *testing.T) {
		pcm, rate, err := decodeWAVFlexibleWithin(
			buildTestWAV(8000, 1, make([]int16, 8000*40)), 30*time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		if rate != 8000 || len(pcm) != 8000*30 {
			t.Fatalf("rate=%d samples=%d", rate, len(pcm))
		}
	})

	t.Run("decoded MP3 bytes are bounded before PCM allocation", func(t *testing.T) {
		raw, err := readDecodedMP3(bytes.NewReader(make([]byte, 4096)), 160)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != 160*4 {
			t.Fatalf("decoded bytes=%d want=%d", len(raw), 160*4)
		}
	})

	t.Run("AMR frame decoding stops at sample limit", func(t *testing.T) {
		framesDecoded := 0
		decoder := &amrDecoderAPI{
			init: func() uintptr { return 1 },
			decode: func(_ uintptr, _ []byte, pcm []int16, _ int) {
				framesDecoded++
				for index := range pcm {
					pcm[index] = 1
				}
			},
			close: func(uintptr) {},
		}
		frame := append([]byte{0x04}, make([]byte, amrNBFrameBytes[0])...)
		data := append([]byte("#!AMR\n"), bytes.Repeat(frame, 10)...)
		pcm, rate, err := decodeAMR(context.Background(), amrDecodeConfig{
			decoder: decoder, data: data, frameSizes: amrNBFrameBytes[:],
			magic: "#!AMR\n", samplesPerFrame: 160, sampleRate: 8000, maxSamples: 240,
		})
		if err != nil {
			t.Fatal(err)
		}
		if rate != 8000 || len(pcm) != 240 || framesDecoded != 2 {
			t.Fatalf("rate=%d samples=%d decoded_frames=%d", rate, len(pcm), framesDecoded)
		}
	})
}

func TestToPCM16WAVDownsamples16kTo8k(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "voice16k.wav")
	samples := make([]int16, 16000)
	for index := range samples {
		samples[index] = 700
	}
	if err := os.WriteFile(source, buildTestWAV(16000, 1, samples), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	target := filepath.Join(dir, "out.wav")
	if err := New().ToPCM16WAV(context.Background(), source, target); err != nil {
		t.Fatalf("ToPCM16WAV: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	pcm, rate, err := decodePCM16WAV(data)
	if err != nil {
		t.Fatalf("decodePCM16WAV: %v", err)
	}
	if rate != 8000 {
		t.Fatalf("sample rate = %d, want 8000", rate)
	}
	if len(pcm) < 7990 || len(pcm) > 8010 {
		t.Fatalf("sample count = %d, want ≈8000", len(pcm))
	}
}
