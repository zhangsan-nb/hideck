package audiotranscode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mp3BitrateNB       = 32
	mp3BitrateWB       = 48
	mp3EncoderQuality  = 3
	mp3MonoMode        = 3
	mp3SamplesPerChunk = 1152
	mp3BufferPadding   = 7200
)

var (
	amrNBFrameBytes = [...]int{12, 13, 15, 17, 19, 20, 26, 31, 5, 0, 0, 0, 0, 0, 0, 0}
	amrWBFrameBytes = [...]int{17, 23, 32, 36, 40, 46, 50, 58, 60, 5, 0, 0, 0, 0, 0, 0}
)

type amrDecoderAPI struct {
	init   func() uintptr
	decode func(uintptr, []byte, []int16, int)
	close  func(uintptr)
}

type lameAPI struct {
	init          func() uintptr
	close         func(uintptr) int
	setSampleRate func(uintptr, int) int
	setChannels   func(uintptr, int) int
	setBitrate    func(uintptr, int) int
	setMode       func(uintptr, int) int
	setQuality    func(uintptr, int) int
	initParams    func(uintptr) int
	encodeBuffer  func(uintptr, []int16, []int16, int, []byte, int) int
	encodeFlush   func(uintptr, []byte, int) int
}

// Transcoder converts the direct call recording through injected system codec libraries.
type Transcoder struct {
	lameOnce          sync.Once
	lame              *lameAPI
	lameErr           error
	amrNBDecoderOnce  sync.Once
	amrNBDecoder      *amrDecoderAPI
	amrNBDecoderErr   error
	amrWBDecoderOnce  sync.Once
	amrWBDecoder      *amrDecoderAPI
	amrWBDecoderErr   error
	amrNBRealtimeOnce sync.Once
	amrNBRealtime     *amrRealtimeAPI
	amrNBRealtimeErr  error
	amrWBRealtimeOnce sync.Once
	amrWBRealtime     *amrRealtimeAPI
	amrWBRealtimeErr  error
}

// New creates a lazy-loading system MP3 transcoder.
func New() *Transcoder { return &Transcoder{} }

// ToMP3 decodes the recorded AMR/WAV source and atomically publishes an MP3 file.
func (t *Transcoder) ToMP3(ctx context.Context, inputPath string) (string, error) {
	if t == nil {
		return "", errors.New("audio transcoder is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.lameOnce.Do(func() { t.lame, t.lameErr = loadNativeLame() })
	if t.lameErr != nil {
		return "", t.lameErr
	}
	pcm, sampleRate, err := t.decodeInput(ctx, strings.TrimSpace(inputPath))
	if err != nil {
		return "", err
	}
	outputPath := replaceAudioExtension(inputPath, ".mp3")
	if err := encodeMP3File(ctx, mp3EncodeRequest{
		api: t.lame, outputPath: outputPath, pcm: pcm, sampleRate: sampleRate,
	}); err != nil {
		return "", err
	}
	return outputPath, nil
}

func replaceAudioExtension(path, extension string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + extension
}

type mp3EncodeRequest struct {
	api        *lameAPI
	outputPath string
	pcm        []int16
	sampleRate int
}

func encodeMP3File(ctx context.Context, request mp3EncodeRequest) (resultErr error) {
	if request.api == nil {
		return errors.New("MP3 encoder is unavailable")
	}
	encoder := request.api.init()
	if encoder == 0 {
		return errors.New("initialize MP3 encoder")
	}
	defer request.api.close(encoder)
	bitrate := mp3BitrateNB
	if request.sampleRate > 8000 {
		bitrate = mp3BitrateWB
	}
	if err := request.api.configure(encoder, request.sampleRate, bitrate); err != nil {
		return err
	}
	temporaryPath := request.outputPath + ".part"
	file, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create MP3 recording: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, file.Close())
		}
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	buffer := make([]byte, mp3SamplesPerChunk*5/4+mp3BufferPadding)
	for offset := 0; offset < len(request.pcm); offset += mp3SamplesPerChunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+mp3SamplesPerChunk, len(request.pcm))
		written := request.api.encode(encoder, request.pcm[offset:end], buffer)
		if err := writeEncodedMP3(file, buffer, written); err != nil {
			return err
		}
	}
	written := request.api.flush(encoder, buffer)
	if err := writeEncodedMP3(file, buffer, written); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close MP3 recording: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, request.outputPath); err != nil {
		return fmt.Errorf("publish MP3 recording: %w", err)
	}
	return nil
}

func writeEncodedMP3(file *os.File, buffer []byte, written int) error {
	if written < 0 {
		return fmt.Errorf("MP3 encoder failed with code %d", written)
	}
	if written == 0 {
		return nil
	}
	if _, err := file.Write(buffer[:written]); err != nil {
		return fmt.Errorf("write MP3 recording: %w", err)
	}
	return nil
}

func (api *lameAPI) configure(encoder uintptr, sampleRate, bitrate int) error {
	settings := []struct {
		name   string
		result int
	}{
		{"sample rate", api.setSampleRate(encoder, sampleRate)},
		{"channel count", api.setChannels(encoder, 1)},
		{"mono mode", api.setMode(encoder, mp3MonoMode)},
		{"bitrate", api.setBitrate(encoder, bitrate)},
		{"quality", api.setQuality(encoder, mp3EncoderQuality)},
		{"parameters", api.initParams(encoder)},
	}
	for _, setting := range settings {
		if setting.result < 0 {
			return fmt.Errorf("configure MP3 %s: code %d", setting.name, setting.result)
		}
	}
	return nil
}

func (api *lameAPI) encode(encoder uintptr, pcm []int16, output []byte) int {
	return api.encodeBuffer(encoder, pcm, pcm, len(pcm), output, len(output))
}

func (api *lameAPI) flush(encoder uintptr, output []byte) int {
	return api.encodeFlush(encoder, output, len(output))
}

// ToPCM16WAV 解码受支持的音频文件并统一写成单声道 PCM16 WAV。
func (t *Transcoder) ToPCM16WAV(ctx context.Context, inputPath, outputPath string) error {
	return t.ToPCM16WAVWithin(ctx, inputPath, outputPath, 0)
}

// ToPCM16WAVWithin 解码并统一写成单声道 PCM16 WAV，超过 maxDuration 的部分截断
// （maxDuration <= 0 表示不限）。
//
// 自动应答提示音只接受 WAV，上传 AMR / AMR-WB / MP3 时由这里转换成目标格式。
// 非 8 kHz / 16 kHz 的源（例如 44.1 kHz 的 MP3）一律降到 8 kHz，与模组声卡一致。
func (t *Transcoder) ToPCM16WAVWithin(ctx context.Context, inputPath, outputPath string, maxDuration time.Duration) error {
	if t == nil {
		return errors.New("audio transcoder is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleaned := strings.TrimSpace(inputPath)
	pcm, sampleRate, err := t.decodeForPrompt(ctx, cleaned, maxDuration)
	if err != nil {
		return err
	}
	if len(pcm) == 0 {
		return errors.New("audio source has no valid PCM data")
	}
	// 目标格式固定为 8 kHz：模组 UAC 只支持 8 kHz，16 kHz 源也统一降下来，
	// 避免播放路径再做一次重采样。
	if sampleRate != 8000 {
		pcm = resampleMono(pcm, sampleRate, 8000)
		sampleRate = 8000
	}
	if maxDuration > 0 {
		if limit := int(maxDuration.Seconds() * float64(sampleRate)); limit > 0 && len(pcm) > limit {
			pcm = pcm[:limit]
		}
	}
	payload, err := EncodePCM16WAV(pcm, sampleRate)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, payload, 0o600)
}

// decodeForPrompt 按扩展名解码提示音来源。WAV 走宽松解析（允许立体声与任意采样率），
// 其余格式沿用录音解码链。
func (t *Transcoder) decodeForPrompt(ctx context.Context, path string, maxDuration time.Duration) ([]int16, int, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".amr" || extension == ".amr-wb" || extension == ".mp3" {
		return t.decodeInputWithin(ctx, path, maxDuration)
	}
	if extension != ".wav" {
		return nil, 0, fmt.Errorf("unsupported prompt audio format %q", extension)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read source audio: %w", err)
	}
	return decodeWAVFlexibleWithin(data, maxDuration)
}

func durationSampleLimit(duration time.Duration, sampleRate int) int {
	if duration <= 0 || sampleRate <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	seconds := int64(duration / time.Second)
	if seconds > int64(maxInt/sampleRate) {
		return maxInt
	}
	whole := int(seconds) * sampleRate
	fraction := int((int64(duration%time.Second) * int64(sampleRate)) / int64(time.Second))
	if fraction > maxInt-whole {
		return maxInt
	}
	return whole + fraction
}
