package audiotranscode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hajimehoshi/go-mp3"
)

// decodeMP3 解码 MP3 并混成单声道、重采样到 8 kHz。
//
// go-mp3 固定输出 16-bit 小端立体声，所以这里顺带混单声道。提示音最终由 8 kHz
// 的模组声卡播放，在解码阶段一次降到 8 kHz，避免播放路径再插一层重采样。
func decodeMP3(data []byte) ([]int16, int, error) {
	return decodeMP3Within(data, 0)
}

func decodeMP3Within(data []byte, maxDuration time.Duration) ([]int16, int, error) {
	decoder, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		// go-mp3 只覆盖 MPEG-1/2。8/11/12 kHz 的语音 MP3 属于 MPEG-2.5，直接给出
		// 可操作的提示，而不是把库的英文错误抛给用户。
		if strings.Contains(err.Error(), "MPEG version 2.5") {
			return nil, 0, errors.New("该 MP3 采样率过低（8/11/12 kHz），请转成 44.1 kHz MP3 或 WAV 后重试")
		}
		return nil, 0, fmt.Errorf("invalid MP3 audio: %w", err)
	}
	rate := decoder.SampleRate()
	if rate <= 0 {
		return nil, 0, errors.New("MP3 audio sample rate is unknown")
	}
	var decoded io.Reader = decoder
	maxFrames := durationSampleLimit(maxDuration, rate)
	raw, err := readDecodedMP3(decoded, maxFrames)
	if err != nil {
		return nil, 0, fmt.Errorf("decode MP3 audio: %w", err)
	}
	if len(raw) < 4 {
		return nil, 0, errors.New("MP3 audio has no valid PCM data")
	}
	frames := len(raw) / 4
	mono := make([]int16, frames)
	for index := 0; index < frames; index++ {
		left := int32(int16(binary.LittleEndian.Uint16(raw[index*4:])))
		right := int32(int16(binary.LittleEndian.Uint16(raw[index*4+2:])))
		mono[index] = int16((left + right) / 2)
	}
	if rate != 8000 {
		mono = resampleMono(mono, rate, 8000)
	}
	if len(mono) == 0 {
		return nil, 0, errors.New("MP3 audio has no valid PCM data")
	}
	return mono, 8000, nil
}

func readDecodedMP3(decoded io.Reader, maxFrames int) ([]byte, error) {
	if maxFrames > 0 {
		maxInt := int(^uint(0) >> 1)
		if maxFrames > maxInt/4 {
			return nil, errors.New("MP3 duration limit is too large")
		}
		decoded = io.LimitReader(decoded, int64(maxFrames*4))
	}
	return io.ReadAll(decoded)
}

// resampleMono 用箱式平均把单声道 PCM 重采样到目标采样率。降采样时逐段平均相当于
// 一个简单低通，足以抑制宽带内容折返；提示音场景不需要更高阶的滤波器。
func resampleMono(pcm []int16, fromRate, toRate int) []int16 {
	if fromRate == toRate || fromRate <= 0 || toRate <= 0 || len(pcm) == 0 {
		return pcm
	}
	ratio := float64(fromRate) / float64(toRate)
	count := int(float64(len(pcm)) / ratio)
	if count <= 0 {
		return nil
	}
	out := make([]int16, count)
	for index := 0; index < count; index++ {
		start := int(float64(index) * ratio)
		end := int(float64(index+1) * ratio)
		if end <= start {
			end = start + 1
		}
		if end > len(pcm) {
			end = len(pcm)
		}
		sum := 0
		for cursor := start; cursor < end; cursor++ {
			sum += int(pcm[cursor])
		}
		out[index] = int16(sum / (end - start))
	}
	return out
}

// EncodePCM16WAV 把单声道 PCM16 样本打包成标准 WAV 文件内容。
func EncodePCM16WAV(pcm []int16, sampleRate int) ([]byte, error) {
	if sampleRate <= 0 {
		return nil, errors.New("WAV sample rate must be positive")
	}
	dataSize := len(pcm) * 2
	buffer := make([]byte, 44+dataSize)
	copy(buffer[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(36+dataSize))
	copy(buffer[8:12], "WAVE")
	copy(buffer[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buffer[16:20], 16)
	binary.LittleEndian.PutUint16(buffer[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buffer[22:24], 1) // 单声道
	binary.LittleEndian.PutUint32(buffer[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buffer[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buffer[32:34], 2)  // 每帧字节数
	binary.LittleEndian.PutUint16(buffer[34:36], 16) // 位深
	copy(buffer[36:40], "data")
	binary.LittleEndian.PutUint32(buffer[40:44], uint32(dataSize))
	for index, sample := range pcm {
		binary.LittleEndian.PutUint16(buffer[44+index*2:], uint16(sample))
	}
	return buffer, nil
}
