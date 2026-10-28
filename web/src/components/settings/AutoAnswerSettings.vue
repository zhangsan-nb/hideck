<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Add20Regular, ArrowUpload24Regular, Subtract20Regular } from '@vicons/fluent'
import { devicesService } from '../../services/devices'
import {
  systemService,
  type AutoAnswerDeviceSettings,
  type AutoAnswerSettings
} from '../../services/system'

type DeviceRow = AutoAnswerDeviceSettings & { name: string }
type SavedDeviceSettings = Pick<AutoAnswerDeviceSettings, 'enabled' | 'delay_seconds'>
const DEFAULT_AUTO_ANSWER_DELAY_SECONDS = 8
const DEVICE_SEARCH_THRESHOLD = 5
const DEVICE_SAVE_BATCH_SIZE = 4

const settings = ref<AutoAnswerSettings>({ audio_ready: false, audio_name: '', devices: [] })
const rows = ref<DeviceRow[]>([])
const loading = ref(true)
const loaded = ref(false)
const loadError = ref('')
const savingDevices = ref(new Set<string>())
const uploading = ref(false)
const searchQuery = ref('')
const savedDevices = ref(new Map<string, SavedDeviceSettings>())
const fileInput = ref<HTMLInputElement | null>(null)

const busy = computed(() => loading.value || uploading.value || savingDevices.value.size > 0)
const enabledCount = computed(() => rows.value.filter(row => row.enabled).length)
const dirtyRows = computed(() => rows.value.filter(isDirty))
const filteredRows = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase()
  if (!query) return rows.value
  return rows.value.filter(row => `${row.name} ${row.device_id}`.toLocaleLowerCase().includes(query))
})

function isDirty(row: DeviceRow) {
  const saved = savedDevices.value.get(row.device_id)
  return !saved || saved.enabled !== row.enabled || saved.delay_seconds !== row.delay_seconds
}

function rememberSaved(rowsToRemember: DeviceRow[]) {
  savedDevices.value = new Map(rowsToRemember.map(row => [
    row.device_id,
    { enabled: row.enabled, delay_seconds: row.delay_seconds }
  ]))
}

function adjustDelay(row: DeviceRow, amount: number) {
  row.delay_seconds = Math.max(1, row.delay_seconds + amount)
}

async function load() {
  loading.value = true
  loaded.value = false
  loadError.value = ''
  const [phoneResult, devicesResult] = await Promise.all([
    systemService.getPhoneSettings(),
    devicesService.listManaged()
  ])
  loading.value = false
  if (!phoneResult.ok) {
    loadError.value = phoneResult.error.message || '自动接听设置加载失败'
    ElMessage.error(loadError.value)
    return
  }
  if (!devicesResult.ok) {
    loadError.value = devicesResult.error.message || '设备列表加载失败'
    ElMessage.error(loadError.value)
    return
  }
  settings.value = phoneResult.data.auto_answer
  const configured = new Map(settings.value.devices.map(item => [item.device_id, item]))
  const loadedRows = devicesResult.data.devices.map(device => {
    const saved = configured.get(device.id)
    return {
      device_id: device.id,
      name: device.name || device.id,
      enabled: saved?.enabled ?? false,
      delay_seconds: saved && saved.delay_seconds > 0 ? saved.delay_seconds : DEFAULT_AUTO_ANSWER_DELAY_SECONDS
    }
  })
  rows.value = loadedRows
  rememberSaved(loadedRows)
  loaded.value = true
}

async function saveAll() {
  const pending = dirtyRows.value.map(row => ({
    row,
    payload: { enabled: row.enabled, delay_seconds: row.delay_seconds }
  }))
  if (!pending.length) return
  savingDevices.value = new Set(pending.map(item => item.row.device_id))
  const results = []
  for (let start = 0; start < pending.length; start += DEVICE_SAVE_BATCH_SIZE) {
    const batch = pending.slice(start, start + DEVICE_SAVE_BATCH_SIZE)
    results.push(...await Promise.all(batch.map(async item => ({
      ...item,
      result: await systemService.saveAutoAnswerDevice(item.row.device_id, item.payload)
    }))))
  }
  savingDevices.value = new Set()

  const nextSaved = new Map(savedDevices.value)
  const failures: Array<{ name: string; message: string }> = []
  for (const item of results) {
    if (item.result.ok) nextSaved.set(item.row.device_id, item.payload)
    else failures.push({ name: item.row.name, message: item.result.error.message || '保存失败' })
  }
  savedDevices.value = nextSaved
  if (failures.length) {
    const first = failures[0]
    const prefix = failures.length > 1 ? `${failures.length} 台设备保存失败；第一项` : ''
    ElMessage.error(`${prefix}${first.name}：${first.message}`)
    return
  }
  ElMessage.success(`已保存 ${results.length} 台设备的自动接听设置`)
}

async function toggleDevice(row: DeviceRow, value: string | number | boolean) {
  const enabled = value === true
  if (!enabled) return
  if (!settings.value.audio_ready) {
    row.enabled = false
    ElMessage.warning('请先上传自动接听录音')
    return
  }
  try {
    await ElMessageBox.confirm(
      `${row.name} 来电持续 ${row.delay_seconds} 秒无人接听时，HiDeck 将自动接听、播放录音、保存对方语音，并发送到 QQ、飞书、Telegram、微信或企业微信机器人。保存修改后生效。`,
      '启用自动接听',
      { type: 'warning', confirmButtonText: '继续', cancelButtonText: '取消' }
    )
  } catch {
    row.enabled = false
    return
  }
}

async function upload(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || uploading.value) return
  uploading.value = true
  const result = await systemService.uploadAutoAnswerAudio(file)
  uploading.value = false
  input.value = ''
  if (!result.ok) {
    ElMessage.error(result.error.message || '自动接听录音上传失败')
    return
  }
  settings.value = {
    ...settings.value,
    audio_ready: result.data.audio_ready,
    audio_name: result.data.audio_name
  }
  ElMessage.success('自动接听录音已更新')
}

onMounted(load)
</script>

<template>
  <div class="auto-answer-settings" :aria-busy="busy">
    <div class="auto-answer-heading">
      <div>
        <div class="text-sm font-bold text-[var(--ui-text)]">未接来电自动应答</div>
        <div class="text-xs leading-relaxed text-[var(--ui-muted)]">
          每台设备独立设置等待时间；默认关闭，不依赖浏览器保持在线。通话结束后，录音会发送到支持附件的 Bot 渠道。
        </div>
      </div>
    </div>

    <div class="auto-answer-audio">
      <div class="auto-answer-audio-copy">
        <strong>共用播放录音</strong>
        <p>支持 WAV / AMR / AMR-WB / MP3，最大 16 MiB；上传后自动转换为单声道 PCM16 WAV（8 kHz），最长 30 秒，超出自动截断。</p>
        <span :class="settings.audio_ready ? 'is-ready' : 'is-missing'" aria-live="polite">
          {{ settings.audio_ready ? `已就绪：${settings.audio_name}` : '尚未上传录音' }}
        </span>
      </div>
      <el-button :loading="uploading" :disabled="!loaded || busy" @click="fileInput?.click()">
        <el-icon><ArrowUpload24Regular /></el-icon>
        {{ uploading ? '正在上传' : '选择音频' }}
      </el-button>
      <input
        ref="fileInput"
        class="auto-answer-file"
        type="file"
        accept=".wav,audio/wav,audio/x-wav,.amr,audio/amr,.amr-wb,.mp3,audio/mpeg"
        :disabled="!loaded || busy"
        @change="upload"
      >
    </div>

    <div v-if="loading" class="auto-answer-state" role="status">正在加载设备设置…</div>
    <div v-else-if="loadError" class="auto-answer-state is-error" role="alert">
      <span>{{ loadError }}</span>
      <el-button :disabled="busy" @click="load">重新加载</el-button>
    </div>
    <section v-else-if="loaded && rows.length" class="auto-answer-device-panel" aria-labelledby="auto-answer-device-title">
      <div class="auto-answer-device-toolbar">
        <div class="auto-answer-device-summary">
          <strong id="auto-answer-device-title">按设备设置</strong>
          <span>
            共 {{ rows.length }} 台，已启用 {{ enabledCount }} 台
            <template v-if="dirtyRows.length"> · {{ dirtyRows.length }} 项未保存</template>
          </span>
        </div>
        <div class="auto-answer-device-actions">
          <el-input
            v-if="rows.length >= DEVICE_SEARCH_THRESHOLD"
            v-model="searchQuery"
            clearable
            aria-label="搜索自动接听设备"
            placeholder="搜索设备"
          />
          <el-button type="primary" :loading="savingDevices.size > 0" :disabled="busy || !dirtyRows.length" @click="saveAll">
            保存修改
          </el-button>
        </div>
      </div>

      <div class="auto-answer-list-header" aria-hidden="true">
        <span>设备</span>
        <span>自动接听</span>
        <span>无人接听后</span>
      </div>
      <div v-if="filteredRows.length" class="auto-answer-device-list" role="list">
        <div
          v-for="row in filteredRows"
          :key="row.device_id"
          class="auto-answer-device-row"
          :class="{ 'is-dirty': isDirty(row) }"
          role="listitem"
        >
          <div class="auto-answer-device-identity">
            <strong :id="`auto-answer-device-${row.device_id}`">{{ row.name }}</strong>
            <span>{{ row.device_id }}</span>
          </div>
          <div class="auto-answer-device-toggle">
            <span class="auto-answer-field-label">自动接听</span>
            <el-switch
              v-model="row.enabled"
              :disabled="busy"
              :aria-label="`${row.name} 自动接听`"
              @change="toggleDevice(row, $event)"
            />
          </div>
          <div class="auto-answer-delay-field">
            <span class="auto-answer-field-label">无人接听后</span>
            <div class="auto-answer-delay-stepper" role="group" :aria-label="`${row.name} 无人接听等待时间`">
              <el-button
                text
                class="auto-answer-stepper-button"
                :disabled="busy || row.delay_seconds <= 1"
                :aria-label="`${row.name} 减少等待时间`"
                @click="adjustDelay(row, -1)"
              >
                <el-icon><Subtract20Regular /></el-icon>
              </el-button>
              <div class="auto-answer-delay-value">
                <el-input-number
                  :id="`auto-answer-delay-${row.device_id}`"
                  v-model="row.delay_seconds"
                  class="font-mono"
                  :min="1"
                  :step="1"
                  :disabled="busy"
                  :controls="false"
                  :aria-label="`${row.name} 无人接听等待秒数`"
                />
                <span class="auto-answer-delay-unit">秒</span>
              </div>
              <el-button
                text
                class="auto-answer-stepper-button"
                :disabled="busy"
                :aria-label="`${row.name} 增加等待时间`"
                @click="adjustDelay(row, 1)"
              >
                <el-icon><Add20Regular /></el-icon>
              </el-button>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="auto-answer-list-empty">未找到匹配设备</div>
    </section>
    <div v-else-if="loaded" class="auto-answer-empty">暂无可配置设备</div>
  </div>
</template>

<style scoped>
.auto-answer-settings { display: grid; gap: 16px; }
.auto-answer-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.auto-answer-audio, .auto-answer-device-panel { min-width: 0; border: 1px solid var(--ui-border); border-radius: var(--ui-radius-md); background: var(--ui-surface-muted); }
.auto-answer-audio { display: flex; align-items: center; gap: 12px; padding: 16px; }
.auto-answer-audio-copy { flex: 1; min-width: 0; }
.auto-answer-audio strong, .auto-answer-device-panel strong { font-size: var(--ui-font-body-sm); font-weight: 650; color: var(--ui-text); }
.auto-answer-audio p { margin: 4px 0 0; color: var(--ui-text-muted); font-size: 13px; line-height: 1.5; }
.auto-answer-audio span { display: block; margin-top: 8px; font-size: 12px; overflow-wrap: anywhere; }
.auto-answer-audio .is-ready { color: var(--ui-success); }
.auto-answer-audio .is-missing { color: var(--ui-warning); }
.auto-answer-device-panel { container-type: inline-size; overflow: hidden; }
.auto-answer-device-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 16px; border-bottom: 1px solid var(--ui-border); background: var(--ui-surface); }
.auto-answer-device-summary { min-width: 0; }
.auto-answer-device-summary span { display: block; margin-top: 3px; color: var(--ui-text-muted); font-size: 12px; }
.auto-answer-device-actions { display: flex; align-items: center; gap: 8px; }
.auto-answer-device-actions .el-input { width: 200px; }
.auto-answer-list-header { display: grid; grid-template-columns: minmax(140px, 1fr) 104px 176px; align-items: center; gap: 16px; padding: 8px 16px; border-bottom: 1px solid var(--ui-border); color: var(--ui-text-muted); font-size: 12px; font-weight: 600; }
.auto-answer-list-header span:nth-child(2) { text-align: center; }
.auto-answer-device-list { max-height: min(440px, 55vh); overflow: auto; scrollbar-gutter: stable; }
.auto-answer-device-row { display: grid; grid-template-columns: minmax(140px, 1fr) 104px 176px; align-items: center; gap: 16px; min-height: 60px; padding: 8px 16px; border-bottom: 1px solid var(--ui-border); }
.auto-answer-device-row.is-dirty { box-shadow: inset 3px 0 0 var(--ui-primary); }
.auto-answer-device-row:last-child { border-bottom: 0; }
.auto-answer-device-identity { min-width: 0; }
.auto-answer-device-identity strong, .auto-answer-device-identity span { display: block; overflow-wrap: anywhere; }
.auto-answer-device-identity span { margin-top: 3px; color: var(--ui-text-muted); font-size: 12px; }
.auto-answer-device-toggle, .auto-answer-delay-field { display: flex; align-items: center; gap: 8px; }
.auto-answer-device-toggle { justify-content: center; }
.auto-answer-field-label { display: none; color: var(--ui-text-muted); font-size: 13px; }
.auto-answer-delay-stepper { display: grid; grid-template-columns: 44px minmax(0, 1fr) 44px; align-items: center; width: 176px; min-height: 44px; box-sizing: border-box; overflow: hidden; border: 1px solid var(--ui-border); border-radius: var(--ui-radius-md); background: var(--ui-surface); }
.auto-answer-delay-stepper:focus-within { border-color: var(--ui-primary); box-shadow: var(--ui-focus); }
.auto-answer-delay-value { min-width: 0; display: flex; align-items: center; justify-content: center; gap: 4px; padding: 0 8px; }
.auto-answer-delay-value .el-input-number, .auto-answer-delay-value :deep(.el-input) { width: 5ch; min-width: 0; min-height: 42px; flex: 0 1 5ch; border-radius: 0 !important; box-shadow: none !important; }
.auto-answer-delay-value :deep(.el-input__wrapper), .auto-answer-delay-value :deep(.el-input__wrapper.is-focus) { min-height: 42px; padding: 0; border-radius: 0 !important; background: transparent; box-shadow: none !important; }
.auto-answer-delay-stepper :deep(.el-input__inner) { min-width: 0; color: var(--ui-text); font-weight: 650; font-variant-numeric: tabular-nums; text-align: center; }
.auto-answer-delay-unit { color: var(--ui-text-muted); font-size: 12px; white-space: nowrap; }
.auto-answer-stepper-button.el-button { width: 44px; min-height: 44px; margin: 0; padding: 0; border: 0; border-radius: 0; color: var(--ui-text-muted); }
.auto-answer-stepper-button:first-child { border-right: 1px solid var(--ui-border); }
.auto-answer-stepper-button:last-child { border-left: 1px solid var(--ui-border); }
.auto-answer-stepper-button:not(.is-disabled):hover, .auto-answer-stepper-button:not(.is-disabled):focus-visible { color: var(--ui-primary); background: var(--ui-surface-muted); }
.auto-answer-state, .auto-answer-empty, .auto-answer-list-empty { padding: 20px; border: 1px dashed var(--ui-border); border-radius: var(--ui-radius-md); color: var(--ui-text-muted); text-align: center; }
.auto-answer-state.is-error { display: flex; align-items: center; justify-content: center; gap: 12px; color: var(--ui-danger); }
.auto-answer-list-empty { border: 0; border-radius: 0; }
.auto-answer-file { display: none; }
:deep(.el-button), :deep(.el-input-number) { min-height: 44px; }
.auto-answer-device-actions :deep(.el-input__wrapper), .auto-answer-delay-field :deep(.el-input__wrapper) { min-height: 36px; }
.auto-answer-device-toggle :deep(.el-switch) { min-width: 44px; min-height: 44px; }
@container (max-width: 620px) {
  .auto-answer-list-header { display: none; }
  .auto-answer-device-row { grid-template-columns: minmax(0, 1fr) auto; gap: 6px 16px; min-height: 0; padding-block: 12px; }
  .auto-answer-device-toggle { gap: 10px; }
  .auto-answer-field-label { display: inline; }
  .auto-answer-delay-field { grid-column: 1 / -1; justify-content: space-between; padding-top: 6px; border-top: 1px solid var(--ui-border); }
}
@container (max-width: 360px) {
  .auto-answer-delay-field { align-items: stretch; flex-direction: column; }
  .auto-answer-delay-stepper { grid-template-columns: 44px minmax(0, 1fr) 44px; width: 100%; }
}
@media (max-width: 720px) {
  .auto-answer-audio { align-items: stretch; flex-direction: column; }
  .auto-answer-audio .el-button { width: 100%; }
  .auto-answer-device-toolbar, .auto-answer-device-actions { align-items: stretch; flex-direction: column; }
  .auto-answer-device-actions .el-input, .auto-answer-device-actions .el-button { width: 100%; }
  .auto-answer-device-list { max-height: none; overflow: visible; }
  .auto-answer-state.is-error { align-items: stretch; flex-direction: column; }
}
</style>
