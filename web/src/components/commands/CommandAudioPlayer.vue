<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import {
  ArrowClockwise24Regular,
  ErrorCircle24Regular,
  MusicNote224Regular,
  Pause24Regular,
  Play24Regular,
  Speaker224Regular,
  SpeakerMute24Regular
} from '@vicons/fluent'
import { commandService } from '../../services/commands'
import type { CommandAttachment } from '../../types/commands'
import { formatPlaybackTime } from '../../utils/audioPlayback'

const props = defineProps<{ attachment: CommandAttachment }>()
const audio = ref<HTMLAudioElement | null>(null)
const source = ref('')
const loading = ref(false)
const error = ref('')
const playing = ref(false)
const currentTime = ref(0)
const duration = ref(0)
const volume = ref(1)
const muted = ref(false)
const silent = computed(() => muted.value || volume.value === 0)
let controller: AbortController | null = null
let lastAudibleVolume = 1

watch(() => props.attachment.recording, loadRecording, { immediate: true })
onUnmounted(releaseRecording)

async function loadRecording(recording: string) {
  releaseRecording()
  resetPlaybackState()
  const requestController = new AbortController()
  controller = requestController
  loading.value = true
  error.value = ''
  const result = await commandService.recording(recording, requestController.signal)
  if (requestController.signal.aborted || controller !== requestController) return
  controller = null
  loading.value = false
  if (!result.ok) {
    error.value = result.error.message || '录音加载失败'
    return
  }
  source.value = URL.createObjectURL(result.data)
}

async function togglePlayback() {
  const player = audio.value
  if (!player || error.value) return
  if (!player.paused) {
    player.pause()
    return
  }
  if (player.ended) player.currentTime = 0
  try {
    await player.play()
  } catch (playError) {
    const reason = playError instanceof Error ? playError.message : '浏览器拒绝播放'
    error.value = `录音播放失败：${reason}`
  }
}

function updateMetadata() {
  const player = audio.value
  if (!player) return
  duration.value = Number.isFinite(player.duration) ? player.duration : 0
  player.volume = volume.value
  player.muted = muted.value
}

function updatePlaybackTime() {
  currentTime.value = audio.value?.currentTime || 0
}

function requireSingleSliderValue(value: number | number[], control: string) {
  if (Array.isArray(value)) throw new TypeError(`${control} 不接受区间值`)
  return value
}

function seekPlayback(value: number | number[]) {
  const player = audio.value
  if (!player) return
  const nextTime = requireSingleSliderValue(value, '录音播放进度')
  player.currentTime = nextTime
  currentTime.value = nextTime
}

function updateVolume(value: number | number[]) {
  const player = audio.value
  if (!player) return
  const nextVolume = requireSingleSliderValue(value, '录音音量')
  volume.value = nextVolume
  player.volume = nextVolume
  if (nextVolume > 0) lastAudibleVolume = nextVolume
  player.muted = nextVolume === 0
  muted.value = player.muted
}

function toggleMuted() {
  const player = audio.value
  if (!player) return
  if (!silent.value) {
    lastAudibleVolume = volume.value
    player.muted = true
    muted.value = true
    return
  }
  if (volume.value === 0) {
    volume.value = lastAudibleVolume
    player.volume = lastAudibleVolume
  }
  player.muted = false
  muted.value = false
}

function handleMediaError() {
  if (!source.value) return
  playing.value = false
  error.value = '录音无法解码或读取，请重试'
}

function resetPlaybackState() {
  playing.value = false
  currentTime.value = 0
  duration.value = 0
}

function releaseRecording() {
  controller?.abort()
  controller = null
  audio.value?.pause()
  if (source.value) URL.revokeObjectURL(source.value)
  source.value = ''
}
</script>

<template>
  <section class="audio-recording" aria-label="通话录音">
    <div class="recording-meta">
      <span class="recording-icon" aria-hidden="true"><el-icon><MusicNote224Regular /></el-icon></span>
      <div class="recording-copy">
        <strong>通话录音</strong>
        <span :title="attachment.recording">{{ attachment.recording }}</span>
      </div>
      <span class="recording-format">MP3</span>
    </div>

    <div v-if="loading" class="audio-state" role="status">
      <el-icon class="is-loading" aria-hidden="true"><ArrowClockwise24Regular /></el-icon>
      <span>正在载入录音</span>
    </div>
    <div v-else-if="error" class="audio-state is-error" role="alert">
      <el-icon aria-hidden="true"><ErrorCircle24Regular /></el-icon>
      <span>{{ error }}</span>
      <el-button class="retry-button" @click="loadRecording(attachment.recording)">重试</el-button>
    </div>
    <div v-else-if="source" class="audio-controls">
      <el-button
        circle
        class="audio-action is-primary"
        :aria-label="playing ? '暂停通话录音' : '播放通话录音'"
        @click="togglePlayback"
      >
        <el-icon><Pause24Regular v-if="playing" /><Play24Regular v-else /></el-icon>
      </el-button>
      <time>{{ formatPlaybackTime(currentTime) }}</time>
      <el-slider
        v-model="currentTime"
        class="playback-range"
        :min="0"
        :max="duration || 0"
        :step="0.1"
        :disabled="!duration"
        :show-tooltip="false"
        aria-label="录音播放进度"
        @input="seekPlayback"
      />
      <time>{{ formatPlaybackTime(duration) }}</time>
      <div class="volume-controls">
        <el-button
          circle
          class="audio-action"
          :aria-label="silent ? '恢复录音声音' : '静音录音'"
          @click="toggleMuted"
        >
          <el-icon><SpeakerMute24Regular v-if="silent" /><Speaker224Regular v-else /></el-icon>
        </el-button>
        <el-slider
          v-model="volume"
          class="volume-range"
          :min="0"
          :max="1"
          :step="0.05"
          :show-tooltip="false"
          aria-label="录音音量"
          @input="updateVolume"
        />
      </div>
      <audio
        ref="audio"
        class="native-audio"
        :src="source"
        preload="metadata"
        @loadedmetadata="updateMetadata"
        @durationchange="updateMetadata"
        @timeupdate="updatePlaybackTime"
        @play="playing = true"
        @pause="playing = false"
        @ended="playing = false"
        @error="handleMediaError"
      />
    </div>
  </section>
</template>

<style scoped>
.audio-recording {
  min-width: 0;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--ui-border-muted);
  display: grid;
  gap: 7px;
}
.recording-meta { min-width: 0; display: flex; align-items: center; gap: 8px; }
.recording-icon {
  width: 26px;
  height: 26px;
  flex: 0 0 26px;
  border: 1px solid var(--ui-border);
  border-radius: var(--ui-radius-sm);
  color: var(--ui-primary);
  display: grid;
  place-items: center;
}
.recording-icon .el-icon { font-size: 15px; }
.recording-copy { min-width: 0; flex: 1; display: flex; align-items: baseline; gap: 8px; }
.recording-copy strong { flex: 0 0 auto; color: var(--ui-text); font-size: var(--ui-font-body-sm); font-weight: 600; }
.recording-copy span {
  min-width: 0;
  overflow: hidden;
  color: var(--ui-muted);
  font: var(--ui-font-body-sm) "v-mono", ui-monospace, monospace;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.recording-format { color: var(--ui-muted); font: var(--ui-font-caption) "v-mono", ui-monospace, monospace; }
.audio-controls {
  min-width: 0;
  display: grid;
  grid-template-columns: 36px auto minmax(80px, 1fr) auto minmax(92px, 124px);
  align-items: center;
  gap: 8px;
}
.audio-action.el-button, .retry-button.el-button {
  margin-left: 0;
  border: 1px solid var(--ui-border);
  background: transparent;
  color: var(--ui-text-muted);
  cursor: pointer;
}
.audio-action.el-button {
  width: 36px;
  height: 36px;
  padding: 0;
  border-radius: 50%;
  display: grid;
  place-items: center;
}
.audio-action.is-primary {
  border-color: color-mix(in srgb, var(--ui-primary) 55%, var(--ui-border));
  background: color-mix(in srgb, var(--ui-primary) 9%, transparent);
  color: var(--ui-primary);
}
.audio-action:hover, .retry-button:hover { border-color: var(--ui-primary); color: var(--ui-primary); }
.audio-action:focus-visible, .retry-button:focus-visible {
  outline: 2px solid var(--ui-primary);
  outline-offset: 2px;
}
.audio-controls time { color: var(--ui-muted); font: var(--ui-font-caption) "v-mono", ui-monospace, monospace; }
.playback-range, .volume-range {
  min-width: 0;
  height: 36px;
  margin: 0;
}
.playback-range.is-disabled { cursor: wait; opacity: .5; }
.playback-range :deep(.el-slider__runway), .volume-range :deep(.el-slider__runway) { height: 4px; background: var(--ui-border); }
.playback-range :deep(.el-slider__bar), .volume-range :deep(.el-slider__bar) { height: 4px; background: var(--ui-primary); }
.playback-range :deep(.el-slider__button), .volume-range :deep(.el-slider__button) { width: 12px; height: 12px; border: 2px solid var(--ui-surface-strong); background: var(--ui-primary); box-shadow: 0 0 0 1px var(--ui-border); }
.volume-controls { min-width: 0; display: grid; grid-template-columns: 36px minmax(48px, 1fr); align-items: center; gap: 7px; }
.native-audio { display: none; }
.audio-state { min-height: 36px; color: var(--ui-text-muted); font-size: var(--ui-font-body-sm); display: flex; align-items: center; gap: 7px; }
.audio-state .el-icon { flex: 0 0 auto; color: var(--ui-primary); font-size: 16px; }
.audio-state.is-error { color: var(--ui-danger); }
.audio-state.is-error .el-icon { color: currentColor; }
.retry-button { min-height: 30px; margin-left: auto; padding: 0 10px; border-radius: var(--ui-radius-pill); }
.is-loading { animation: audio-loading 900ms linear infinite; }
@keyframes audio-loading { to { transform: rotate(360deg); } }
@media (max-width: 640px) {
  .recording-copy { display: grid; gap: 1px; }
  .audio-controls { grid-template-columns: 44px auto minmax(32px, 1fr) auto; gap: 7px; }
  .audio-action { width: 44px; height: 44px; }
  .playback-range, .volume-range { height: 44px; }
  .volume-controls { grid-column: 1 / -1; grid-template-columns: 44px minmax(0, 1fr); gap: 8px; }
  .retry-button { min-height: 44px; }
}
@media (prefers-reduced-motion: reduce) {
  .is-loading { animation: none; }
}
</style>
