<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Call24Regular,
  CallInbound24Regular,
  CallMissed24Regular,
  CallOutbound24Regular,
  Delete24Regular,
  MoreHorizontal24Regular,
  PersonAdd24Regular
} from '@vicons/fluent'
import type { PhoneRecord } from '../services/phone'
import { usePhoneStore } from '../stores/phone'
import { usePhoneIdentity } from '../composables/usePhoneIdentity'
import { phoneContactsService } from '../services/phone-contacts'
import { formatCallTime, formatRecordDuration, phoneErrorMessage, phoneRecordStatusLabel } from '../utils/phone'

const props = defineProps<{ records: PhoneRecord[] }>()
const identities = usePhoneIdentity()

const phone = usePhoneStore()
const managing = ref(false)
const deleting = ref(false)
const selectedIDs = ref<number[]>([])
const activeStatuses = new Set(['calling', 'ringing', 'waiting', 'connected'])

const deletableRecords = computed(() => props.records.filter((record) => !activeStatuses.has(record.status)))
const selectedRecords = computed(() => {
  const selected = new Set(selectedIDs.value)
  return deletableRecords.value.filter((record) => selected.has(record.id))
})
const allSelected = computed(() => (
  deletableRecords.value.length > 0 && selectedRecords.value.length === deletableRecords.value.length
))

watch(() => props.records.map((item) => `${item.device_id}\u0000${item.peer}`).join('|'), () => {
  for (const record of props.records) {
    if (record.peer) void identities.resolve(record.peer, record.device_id)
  }
}, { immediate: true })

async function saveContact(record: PhoneRecord) {
  const peer = String(record.peer || '').trim()
  if (!peer) return
  const current = identities.identityFor(peer, record.device_id)
  try {
    const { value } = await ElMessageBox.prompt('保存后，来电和通话记录会显示这个名字', '加到联系人', {
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputPlaceholder: '联系人名字',
      inputValue: current?.name || '',
      inputValidator: (v) => !!String(v || '').trim() || '请填写名字'
    })
    const ident = await phoneContactsService.save({
      number: peer,
      name: String(value).trim(),
      deviceId: record.device_id,
      contactId: current?.contact_id
    })
    identities.upsertLocal(ident, peer, record.device_id)
    ElMessage.success('已保存联系人')
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error instanceof Error ? error.message : '保存联系人失败')
  }
}
// 录音播放：用原生 <audio controls> 展示，进度条/拖动/音量/时长都是浏览器
// 自带的，不再自己维护播放状态。录音接口需要 Bearer 鉴权，<audio src> 带不了
// Authorization 头，所以 src 仍是 blob：滚动到可见时才异步换取，避免一次拉上百
// 条录音。
const recordingSources = ref<Record<number, string>>({})
const recordingErrors = ref<Record<number, string>>({})
const loadingRecordings = new Map<number, string>()
const recordingNames = new Map<number, string>()
const recordingErrorNames = new Map<number, string>()
const audioElements = new Map<number, HTMLAudioElement>()
const redialing = ref(false)
let observer: IntersectionObserver | null = null

function withoutRecord<T>(values: Record<number, T>, id: number): Record<number, T> {
  return Object.fromEntries(Object.entries(values).filter(([key]) => Number(key) !== id)) as Record<number, T>
}

function statusIcon(record: PhoneRecord) {
  if (record.status === 'missed' || record.status === 'rejected' || record.status === 'busy' || record.status === 'failed') {
    return CallMissed24Regular
  }
  return record.direction === 'inbound' ? CallInbound24Regular : CallOutbound24Regular
}

// 回拨：复用拨号流程，直接以该记录的对端号码发起双向呼叫。
function redial(record: PhoneRecord) {
  const peer = String(record.peer || '').trim()
  if (!peer || redialing.value || phone.currentCall) return
  redialing.value = true
  void phone.startCall(record.device_id, peer)
    .catch((error) => {
      ElMessage.error(error instanceof Error ? error.message : '回拨失败')
    })
    .finally(() => { redialing.value = false })
}

function setSelected(id: number, checked: boolean) {
  if (checked) {
    if (!selectedIDs.value.includes(id)) selectedIDs.value = [...selectedIDs.value, id]
    return
  }
  selectedIDs.value = selectedIDs.value.filter((selected) => selected !== id)
}

function toggleSelectAll() {
  selectedIDs.value = allSelected.value ? [] : deletableRecords.value.map((record) => record.id)
}

function toggleManaging() {
  managing.value = !managing.value
  selectedIDs.value = []
}

async function deleteRecords(records: PhoneRecord[]) {
  if (!records.length || deleting.value) return
  const count = records.length
  deleting.value = true
  try {
    await ElMessageBox.confirm(
      count === 1
        ? '确定删除这条通话记录吗？联系人和录音文件不会受影响。'
        : `确定删除选中的 ${count} 条通话记录吗？联系人和录音文件不会受影响。`,
      count === 1 ? '删除通话记录' : '批量删除通话记录',
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        type: 'warning'
      }
    )
    const deleted = await phone.deleteHistory(records.map((record) => record.id))
    const deletedIDs = new Set(records.map((record) => record.id))
    selectedIDs.value = selectedIDs.value.filter((id) => !deletedIDs.has(id))
    ElMessage.success(deleted > 0 ? `已删除 ${deleted} 条通话记录` : '所选通话记录已不存在')
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(phoneErrorMessage(error, '删除通话记录失败'))
  } finally {
    deleting.value = false
  }
}

function handleRecordCommand(record: PhoneRecord, command: string | number | object) {
  if (command === 'redial') redial(record)
  else if (command === 'contact') void saveContact(record)
  else if (command === 'delete') void deleteRecords([record])
}

async function ensureRecording(record: PhoneRecord) {
  const name = String(record.recording_name || '').trim()
  if (!name) return
  if (recordingSources.value[record.id] && recordingNames.get(record.id) === name) return
  if (loadingRecordings.get(record.id) === name) return
  loadingRecordings.set(record.id, name)
  recordingErrors.value = withoutRecord(recordingErrors.value, record.id)
  recordingErrorNames.delete(record.id)
  try {
    const url = await phone.recordingURL(name)
    const current = props.records.find((item) => item.id === record.id)
    if (loadingRecordings.get(record.id) !== name || current?.recording_name !== name) {
      URL.revokeObjectURL(url)
      return
    }
    releaseRecording(record.id)
    recordingNames.set(record.id, name)
    recordingSources.value = { ...recordingSources.value, [record.id]: url }
  } catch (error) {
    if (loadingRecordings.get(record.id) === name) {
      recordingErrors.value = {
        ...recordingErrors.value,
        [record.id]: phoneErrorMessage(error, '录音加载失败')
      }
      recordingErrorNames.set(record.id, name)
    }
  } finally {
    if (loadingRecordings.get(record.id) === name) loadingRecordings.delete(record.id)
  }
}

function releaseRecording(id: number) {
  const url = recordingSources.value[id]
  if (url) URL.revokeObjectURL(url)
  recordingSources.value = withoutRecord(recordingSources.value, id)
  recordingNames.delete(id)
}

function syncRecordingState() {
  const current = new Map(props.records.map((record) => [record.id, String(record.recording_name || '').trim()]))
  for (const [id, name] of recordingNames) {
    if (current.get(id) !== name) releaseRecording(id)
  }
  const errors = Object.fromEntries(Object.entries(recordingErrors.value).filter(([value]) => {
    const id = Number(value)
    return current.get(id) === recordingErrorNames.get(id)
  }))
  recordingErrors.value = errors
  for (const id of recordingErrorNames.keys()) {
    if (current.get(id) !== recordingErrorNames.get(id)) recordingErrorNames.delete(id)
  }
  observeRecordings()
}

function registerAudio(record: PhoneRecord, element: unknown) {
  const audio = element as HTMLAudioElement | null
  if (!audio) {
    const previous = audioElements.get(record.id)
    if (previous) observer?.unobserve(previous)
    audioElements.delete(record.id)
    return
  }
  audioElements.set(record.id, audio)
  observer?.observe(audio)
}

function observeRecordings() {
  if (!observer) return
  for (const element of audioElements.values()) observer.observe(element)
}

onMounted(() => {
  if (typeof IntersectionObserver === 'undefined') {
    for (const record of props.records) void ensureRecording(record)
    return
  }
  observer = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue
      const element = entry.target as HTMLAudioElement
      const id = Number(element.dataset.recordId || '')
      const record = props.records.find((item) => item.id === id)
      if (record) void ensureRecording(record)
      observer?.unobserve(element)
    }
  }, { rootMargin: '200px' })
  observeRecordings()
})

watch(() => props.records, (records) => {
  const deletable = new Set(records.filter((record) => !activeStatuses.has(record.status)).map((record) => record.id))
  selectedIDs.value = selectedIDs.value.filter((id) => deletable.has(id))
  syncRecordingState()
}, { flush: 'post' })

onUnmounted(() => {
  observer?.disconnect()
  observer = null
  for (const url of Object.values(recordingSources.value)) URL.revokeObjectURL(url)
  recordingSources.value = {}
  recordingErrors.value = {}
  recordingNames.clear()
  recordingErrorNames.clear()
  loadingRecordings.clear()
  audioElements.clear()
})
</script>

<template>
  <section class="history-panel" aria-labelledby="phone-history-title">
    <header>
      <div>
        <span>CALL LOG</span>
        <h2 id="phone-history-title">最近通话</h2>
      </div>
      <div class="history-header-actions">
        <strong>{{ records.length }}</strong>
        <el-button v-if="records.length" text :disabled="deleting" @click="toggleManaging">
          {{ managing ? '完成' : '管理' }}
        </el-button>
      </div>
    </header>
    <div v-if="records.length" class="history-list">
      <div v-if="managing" class="history-bulk-bar" role="toolbar" aria-label="批量管理通话记录">
        <span>已选 {{ selectedRecords.length }} 条</span>
        <div>
          <el-button text :disabled="!deletableRecords.length || deleting" @click="toggleSelectAll">
            {{ allSelected ? '取消全选' : '全选' }}
          </el-button>
          <el-button
            type="danger"
            plain
            :loading="deleting"
            :disabled="!selectedRecords.length"
            @click="deleteRecords(selectedRecords)"
          >
            <el-icon><Delete24Regular /></el-icon>
            删除
          </el-button>
        </div>
      </div>
      <article v-for="record in records" :key="record.id" class="history-item">
        <div v-if="managing" class="history-selection">
          <el-checkbox
            :model-value="selectedIDs.includes(record.id)"
            :disabled="activeStatuses.has(record.status) || deleting"
            :title="activeStatuses.has(record.status) ? '通话结束后可删除' : undefined"
            :aria-label="`选择 ${record.peer || '未知号码'} 的通话记录`"
            @change="setSelected(record.id, $event === true)"
          />
        </div>
        <div class="history-icon" :class="`is-${record.status}`">
          <el-icon><component :is="statusIcon(record)" /></el-icon>
        </div>
        <div class="history-copy">
          <div class="history-primary">
            <strong>{{ identities.titleFor(record.peer, record.device_id) }}</strong>
            <time>{{ formatCallTime(record.started_at) }}</time>
          </div>
          <div class="history-secondary">
            <span :class="`status-${record.status}`">{{ phoneRecordStatusLabel(record) }}</span>
            <span>{{ formatRecordDuration(record) }}</span>
            <span>{{ record.device_id }}</span>
          </div>
          <p v-if="identities.subtitleFor(record.peer, record.device_id)" class="history-attribution">{{ identities.subtitleFor(record.peer, record.device_id) }}</p>
          <p v-if="record.recording_error" class="recording-error">录音失败：{{ record.recording_error }}</p>
          <p v-else-if="recordingErrors[record.id]" class="recording-error">{{ recordingErrors[record.id] }}</p>
          <audio
            v-if="record.recording_name"
            :ref="(el) => registerAudio(record, el)"
            :data-record-id="record.id"
            class="recording-player"
            controls
            preload="none"
            :src="recordingSources[record.id] || undefined"
          />
        </div>
        <el-dropdown
          v-if="!managing"
          trigger="click"
          placement="bottom-end"
          @command="handleRecordCommand(record, $event)"
        >
          <button type="button" class="record-menu-button" :aria-label="`${record.peer || '未知号码'} 的通话操作`">
            <el-icon><MoreHorizontal24Regular /></el-icon>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="redial" :disabled="!record.peer || redialing || !!phone.currentCall">
                <el-icon><Call24Regular /></el-icon>回拨
              </el-dropdown-item>
              <el-dropdown-item command="contact" :disabled="!record.peer">
                <el-icon><PersonAdd24Regular /></el-icon>添加联系人
              </el-dropdown-item>
              <el-dropdown-item command="delete" divided :disabled="activeStatuses.has(record.status) || deleting">
                <span class="danger-menu-item">
                  <el-icon><Delete24Regular /></el-icon>
                  {{ activeStatuses.has(record.status) ? '通话结束后可删除' : '删除' }}
                </span>
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </article>
    </div>
    <div v-else class="history-empty">暂无通话记录</div>
  </section>
</template>

<style scoped>
.history-panel { min-height: 320px; overflow: hidden; background: transparent; }
.history-panel > header { min-height: 68px; padding: 14px 16px; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid var(--ui-border); }
.history-panel header span { color: var(--ui-primary); font-family: "v-mono", monospace; font-size: 9px; font-weight: 700; letter-spacing: .12em; }
.history-panel h2 { margin: 2px 0 0; color: var(--ui-text); font-size: 16px; }
.history-header-actions { display: flex; align-items: center; gap: 6px; }
.history-header-actions > strong { min-width: 28px; padding: 3px 8px; border-radius: 20px; background: var(--ui-surface-muted); color: var(--ui-text-muted); text-align: center; }
.history-list { max-height: 560px; overflow-y: auto; }
.history-bulk-bar { min-height: 58px; padding: 8px 14px; position: sticky; top: 0; z-index: 2; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--ui-border); background: color-mix(in srgb, var(--ui-surface) 94%, transparent); backdrop-filter: blur(12px); }
.history-bulk-bar > span { color: var(--ui-text); font-size: 13px; font-weight: 700; white-space: nowrap; }
.history-bulk-bar > div { display: flex; align-items: center; gap: 8px; }
.history-item { min-height: 76px; padding: 12px 14px; display: flex; align-items: flex-start; gap: 11px; border-bottom: 1px solid var(--ui-border-muted); }
.history-selection { width: 44px; min-height: 44px; flex: 0 0 44px; display: grid; place-items: center; }
.history-selection :deep(.el-checkbox) { width: 44px; height: 44px; display: grid; place-items: center; }
.history-icon { width: 34px; height: 34px; flex: 0 0 34px; display: grid; place-items: center; border-radius: 50%; background: color-mix(in srgb, var(--ui-primary) 10%, var(--ui-surface)); color: var(--ui-primary); }
.history-icon.is-missed, .history-icon.is-rejected, .history-icon.is-failed { color: var(--ui-danger); background: color-mix(in srgb, var(--ui-danger) 10%, var(--ui-surface)); }
.history-attribution { margin: 4px 0 0; color: var(--ui-text-muted); font-size: 12px; }
.history-icon.is-busy { color: var(--ui-warning); }
.history-copy { min-width: 0; flex: 1; }
.history-primary { display: flex; justify-content: space-between; gap: 8px; }
.history-primary strong { overflow: hidden; text-overflow: ellipsis; color: var(--ui-text); font-family: "v-mono", monospace; font-size: 13px; white-space: nowrap; }
.history-primary time { color: var(--ui-text-muted); font-size: 11px; white-space: nowrap; }
.history-secondary { margin-top: 4px; display: flex; flex-wrap: wrap; gap: 5px 10px; color: var(--ui-text-muted); font-size: 11px; }
.status-missed, .status-rejected, .status-failed { color: var(--ui-danger); }
.status-busy { color: var(--ui-warning); }
.recording-error { margin: 5px 0 0; color: var(--ui-danger); font-size: 11px; line-height: 1.35; }
.record-menu-button { width: 44px; height: 44px; flex: 0 0 44px; display: grid; place-items: center; border: 1px solid var(--ui-border); border-radius: 50%; background: var(--ui-surface); color: var(--ui-text-muted); cursor: pointer; }
.record-menu-button:hover, .record-menu-button:focus-visible { border-color: var(--ui-primary); color: var(--ui-primary); outline: none; }
.danger-menu-item { width: 100%; display: inline-flex; align-items: center; gap: 8px; color: var(--ui-danger); }
.recording-player { width: 100%; max-width: 320px; margin-top: 6px; display: block; }
@media (max-width: 640px) {
  .history-item { padding: 10px 12px; gap: 8px; }
  .history-bulk-bar { padding-inline: 10px; }
  .recording-player { max-width: 100%; }
}
.history-empty { min-height: 250px; display: grid; place-items: center; color: var(--ui-text-muted); }
</style>
