package audiotranscode

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DecodePCM decodes a supported recording into mono PCM samples.
func (t *Transcoder) DecodePCM(ctx context.Context, inputPath string) ([]int16, int, error) {
	if t == nil {
		return nil, 0, errors.New("audio transcoder is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return t.decodeInput(ctx, strings.TrimSpace(inputPath))
}

func (t *Transcoder) decodeInput(ctx context.Context, inputPath string) ([]int16, int, error) {
	return t.decodeInputWithin(ctx, inputPath, 0)
}

func (t *Transcoder) decodeInputWithin(ctx context.Context, inputPath string, maxDuration time.Duration) ([]int16, int, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, 0, fmt.Errorf("read source recording: %w", err)
	}
	switch strings.ToLower(filepath.Ext(inputPath)) {
	case ".amr":
		decoder, err := t.recordingDecoder("AMR")
		if err != nil {
			return nil, 0, err
		}
		return decodeAMR(ctx, amrDecodeConfig{
			decoder: decoder, data: data, frameSizes: amrNBFrameBytes[:],
			magic: "#!AMR\n", samplesPerFrame: 160, sampleRate: 8000,
			maxSamples: durationSampleLimit(maxDuration, 8000),
		})
	case ".amr-wb":
		decoder, err := t.recordingDecoder("AMR-WB")
		if err != nil {
			return nil, 0, err
		}
		return decodeAMR(ctx, amrDecodeConfig{
			decoder: decoder, data: data, frameSizes: amrWBFrameBytes[:],
			magic: "#!AMR-WB\n", samplesPerFrame: 320, sampleRate: 16000,
			maxSamples: durationSampleLimit(maxDuration, 16000),
		})
	case ".wav":
		return decodePCM16WAV(data)
	case ".mp3":
		return decodeMP3Within(data, maxDuration)
	case ".evs":
		decoder, err := t.recordingDecoder("AMR-WB")
		if err != nil {
			return nil, 0, err
		}
		return decodeEVSRecording(ctx, decoder, data)
	default:
		return nil, 0, fmt.Errorf("unsupported recording format %q", filepath.Ext(inputPath))
	}
}

func (t *Transcoder) recordingDecoder(codec string) (*amrDecoderAPI, error) {
	if codec == "AMR" {
		t.amrNBDecoderOnce.Do(func() {
			t.amrNBDecoder, t.amrNBDecoderErr = loadRecordingAMRDecoder(codec)
		})
		return t.amrNBDecoder, t.amrNBDecoderErr
	}
	t.amrWBDecoderOnce.Do(func() {
		t.amrWBDecoder, t.amrWBDecoderErr = loadRecordingAMRDecoder(codec)
	})
	return t.amrWBDecoder, t.amrWBDecoderErr
}

type amrDecodeConfig struct {
	decoder         *amrDecoderAPI
	data            []byte
	frameSizes      []int
	magic           string
	samplesPerFrame int
	sampleRate      int
	maxSamples      int
}

func decodeAMR(ctx context.Context, config amrDecodeConfig) ([]int16, int, error) {
	if config.decoder == nil {
		return nil, 0, errors.New("AMR decoder is unavailable")
	}
	if !strings.HasPrefix(string(config.data), config.magic) {
		return nil, 0, errors.New("invalid AMR recording header")
	}
	state := config.decoder.init()
	if state == 0 {
		return nil, 0, errors.New("initialize AMR decoder")
	}
	defer config.decoder.close(state)
	offset := len(config.magic)
	capacity := len(config.data) * 4
	if config.maxSamples > 0 && capacity > config.maxSamples {
		capacity = config.maxSamples
	}
	pcm := make([]int16, 0, capacity)
	for offset < len(config.data) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		frameType := int((config.data[offset] >> 3) & 0x0f)
		if config.frameSizes[frameType] == 0 && frameType < 14 {
			return nil, 0, fmt.Errorf("unsupported AMR frame type %d", frameType)
		}
		frameLength := 1 + config.frameSizes[frameType]
		if offset+frameLength > len(config.data) {
			return nil, 0, errors.New("truncated AMR recording frame")
		}
		framePCM := make([]int16, config.samplesPerFrame)
		badFrame := 0
		if config.data[offset]&0x04 == 0 {
			badFrame = 1
		}
		config.decoder.decode(state, config.data[offset:offset+frameLength], framePCM, badFrame)
		if config.maxSamples > 0 && len(pcm)+len(framePCM) > config.maxSamples {
			framePCM = framePCM[:config.maxSamples-len(pcm)]
		}
		pcm = append(pcm, framePCM...)
		offset += frameLength
		if config.maxSamples > 0 && len(pcm) >= config.maxSamples {
			break
		}
	}
	if len(pcm) == 0 {
		return nil, 0, errors.New("AMR recording contains no audio frames")
	}
	return pcm, config.sampleRate, nil
}

const evsRecordingMagic = "#!EVS_MC1.0\n"

func decodeEVSRecording(ctx context.Context, decoder *amrDecoderAPI, data []byte) ([]int16, int, error) {
	if decoder == nil {
		return nil, 0, errors.New("AMR-WB decoder is unavailable")
	}
	if !strings.HasPrefix(string(data), evsRecordingMagic) {
		return nil, 0, errors.New("invalid EVS recording header")
	}
	state := decoder.init()
	if state == 0 {
		return nil, 0, errors.New("initialize AMR-WB decoder")
	}
	defer decoder.close(state)
	offset := len(evsRecordingMagic)
	pcm := make([]int16, 0, len(data)*4)
	for offset < len(data) {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if offset+2 > len(data) {
			return nil, 0, errors.New("truncated EVS recording frame")
		}
		bits := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2
		frameBytes := (bits + 7) / 8
		if bits <= 0 || offset+frameBytes > len(data) {
			return nil, 0, errors.New("truncated EVS recording frame")
		}
		storage, err := evsIOPayloadToStorageBits(data[offset:offset+frameBytes], bits)
		if err != nil {
			return nil, 0, err
		}
		offset += frameBytes
		framePCM := make([]int16, 320)
		badFrame := 0
		if storage[0]&amrQualityBit == 0 {
			badFrame = 1
		}
		decoder.decode(state, storage, framePCM, badFrame)
		pcm = append(pcm, framePCM...)
	}
	if len(pcm) == 0 {
		return nil, 0, errors.New("EVS recording contains no audio frames")
	}
	return pcm, 16000, nil
}

func decodePCM16WAV(data []byte) ([]int16, int, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New("invalid WAV recording header")
	}
	var sampleRate int
	var pcmBytes []byte
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start, end := offset+8, offset+8+size
		if end > len(data) {
			return nil, 0, errors.New("truncated WAV chunk")
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if size < 16 || binary.LittleEndian.Uint16(data[start:start+2]) != 1 ||
				binary.LittleEndian.Uint16(data[start+2:start+4]) != 1 ||
				binary.LittleEndian.Uint16(data[start+14:start+16]) != 16 {
				return nil, 0, errors.New("WAV recording must be mono PCM16")
			}
			sampleRate = int(binary.LittleEndian.Uint32(data[start+4 : start+8]))
		case "data":
			pcmBytes = data[start:end]
		}
		offset = end + size%2
	}
	if sampleRate <= 0 || len(pcmBytes) == 0 || len(pcmBytes)%2 != 0 {
		return nil, 0, errors.New("WAV recording has no valid PCM data")
	}
	pcm := make([]int16, len(pcmBytes)/2)
	for index := range pcm {
		pcm[index] = int16(binary.LittleEndian.Uint16(pcmBytes[index*2:]))
	}
	return pcm, sampleRate, nil
}
