package audiotranscode

import (
	"encoding/binary"
	"errors"
	"time"
)

// decodeWAVFlexible 解析常见的 WAV 变体（16-bit PCM、单声道或立体声、任意采样率）
// 并混成单声道。
//
// 自动应答提示音允许用户直接上传手机录音，这类文件通常是 44.1 kHz 立体声，比
// 录音转码用的严格格式（8 kHz 单声道）宽得多，因此这里单独做一层宽松解析。
func decodeWAVFlexible(data []byte) ([]int16, int, error) {
	return decodeWAVFlexibleWithin(data, 0)
}

func decodeWAVFlexibleWithin(data []byte, maxDuration time.Duration) ([]int16, int, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New("invalid WAV audio header")
	}
	var (
		audioFormat   uint16
		channels      uint16
		sampleRate    int
		bitsPerSample uint16
		pcmBytes      []byte
	)
	for offset := 12; offset+8 <= len(data); {
		size := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start, end := offset+8, len(data)
		if size <= uint64(len(data)-start) {
			end = start + int(size)
		} else {
			// 部分编辑器写出的 WAV 头长度与实际数据不一致，按实际长度截断即可。
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if end-start < 16 {
				return nil, 0, errors.New("WAV audio has an invalid fmt chunk")
			}
			audioFormat = binary.LittleEndian.Uint16(data[start : start+2])
			channels = binary.LittleEndian.Uint16(data[start+2 : start+4])
			sampleRate = int(binary.LittleEndian.Uint32(data[start+4 : start+8]))
			bitsPerSample = binary.LittleEndian.Uint16(data[start+14 : start+16])
		case "data":
			pcmBytes = data[start:end]
		}
		if size == 0 {
			break
		}
		offset = end + int(size%2)
	}
	if audioFormat != 1 {
		return nil, 0, errors.New("WAV audio must be uncompressed PCM")
	}
	if channels != 1 && channels != 2 {
		return nil, 0, errors.New("WAV audio must be mono or stereo")
	}
	if bitsPerSample != 16 {
		return nil, 0, errors.New("WAV audio must be 16-bit PCM")
	}
	if sampleRate <= 0 {
		return nil, 0, errors.New("WAV audio sample rate is unknown")
	}
	bytesPerFrame := 2 * int(channels)
	if bytesPerFrame <= 0 || len(pcmBytes) < bytesPerFrame {
		return nil, 0, errors.New("WAV audio has no valid PCM data")
	}
	frames := len(pcmBytes) / bytesPerFrame
	if limit := durationSampleLimit(maxDuration, sampleRate); limit > 0 && frames > limit {
		frames = limit
	}
	mono := make([]int16, frames)
	for index := 0; index < frames; index++ {
		base := index * bytesPerFrame
		left := int32(int16(binary.LittleEndian.Uint16(pcmBytes[base:])))
		if channels == 1 {
			mono[index] = int16(left)
			continue
		}
		right := int32(int16(binary.LittleEndian.Uint16(pcmBytes[base+2:])))
		mono[index] = int16((left + right) / 2)
	}
	return mono, sampleRate, nil
}
