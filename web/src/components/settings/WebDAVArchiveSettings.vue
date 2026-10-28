<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete20Regular, Save24Regular, Server24Regular } from '@vicons/fluent'
import {
  systemService,
  type ArchiveCleanupPreview,
  type ArchiveSettings,
  type ArchiveSettingsPayload,
  type ArchiveStatus,
} from '../../services/system'

const emptyStatus = (): ArchiveStatus => ({
  running: false,
  last_summary: { uploaded: 0, skipped: 0, bytes: 0 },
})

const form = reactive<ArchiveSettingsPayload>({
  enabled: false,
  endpoint: '',
  username: '',
  password: '',
  clear_password: false,
  base_path: 'hideck',
  interval_hours: 24,
  retention_days: 30,
  auto_cleanup: false,
  include_sms: true,
  include_calls: true,
  include_recordings: true,
})
const status = ref<ArchiveStatus>(emptyStatus())
const passwordSet = ref(false)
const cleanupPreview = ref<ArchiveCleanupPreview | null>(null)
const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
const running = ref(false)
const previewing = ref(false)
const cleaning = ref(false)
const savedSnapshot = ref('')

const busy = computed(() => saving.value || testing.value || running.value || previewing.value || cleaning.value || status.value.running)
const contentSelected = computed(() => form.include_sms || form.include_calls || form.include_recordings)
const hasUnsavedChanges = computed(() => savedSnapshot.value !== serializeForm())

function applySettings(settings: ArchiveSettings) {
  Object.assign(form, {
    enabled: settings.enabled,
    endpoint: settings.endpoint,
    username: settings.username,
    password: '',
    clear_password: false,
    base_path: settings.base_path,
    interval_hours: settings.interval_hours,
    retention_days: settings.retention_days,
    auto_cleanup: settings.auto_cleanup,
    include_sms: settings.include_sms,
    include_calls: settings.include_calls,
    include_recordings: settings.include_recordings,
  })
  passwordSet.value = settings.password_set
  status.value = settings.status || emptyStatus()
  savedSnapshot.value = serializeForm()
}

function serializeForm() {
  return JSON.stringify(form)
}

async function loadSettings() {
  loading.value = true
  const result = await systemService.getArchiveSettings()
  loading.value = false
  if (!result.ok) {
    ElMessage.error(result.error.message || '归档设置加载失败')
    return
  }
  applySettings(result.data)
}

async function saveSettings() {
  if (!contentSelected.value) {
    ElMessage.warning('请至少选择一种归档内容')
    return
  }
  saving.value = true
  const result = await systemService.saveArchiveSettings({ ...form })
  saving.value = false
  if (!result.ok) {
    ElMessage.error(result.error.message || '归档设置保存失败')
    return
  }
  applySettings(result.data)
  cleanupPreview.value = null
  ElMessage.success('归档设置已保存')
}

async function testConnection() {
  testing.value = true
  const result = await systemService.testArchiveSettings({ ...form })
  testing.value = false
  if (!result.ok) {
    ElMessage.error(result.error.message || 'WebDAV 连接失败')
    return
  }
  ElMessage.success('WebDAV 连接正常')
}

async function runArchive() {
  running.value = true
  const result = await systemService.runArchive()
  running.value = false
  if (!result.ok) {
    ElMessage.error(result.error.message || '归档失败')
    return
  }
  ElMessage.success(`归档完成：上传 ${result.data.uploaded} 项，跳过 ${result.data.skipped} 项`)
  await loadSettings()
}

async function previewCleanup() {
  previewing.value = true
  const result = await systemService.previewArchiveCleanup()
  previewing.value = false
  if (!result.ok) {
    ElMessage.error(result.error.message || '清理预览失败')
    return
  }
  cleanupPreview.value = result.data
}

async function confirmCleanup() {
  const preview = cleanupPreview.value
  if (!preview) return
  try {
    await ElMessageBox.confirm(
      `将删除 ${preview.sms_count} 条短信、${preview.call_count} 条通话记录和 ${preview.recording_count} 个录音。仅删除已成功归档且超过保留期的数据。`,
      '确认清理本地数据',
      { confirmButtonText: '确认清理', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  cleaning.value = true
  const result = await systemService.cleanupArchive(preview.token)
  cleaning.value = false
  if (!result.ok) {
    cleanupPreview.value = null
    ElMessage.error(result.error.message || '清理失败')
    return
  }
  cleanupPreview.value = null
  ElMessage.success('本地已归档数据清理完成')
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function formatDate(value?: string) {
  if (!value) return '尚未运行'
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

onMounted(loadSettings)
</script>

<template>
  <section class="archive-card p-5 sm:p-6" aria-labelledby="webdav-archive-title">
    <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex min-w-0 items-center gap-3">
        <div class="archive-icon"><el-icon size="24"><Server24Regular /></el-icon></div>
        <div class="min-w-0">
          <h3 id="webdav-archive-title" class="text-lg font-bold text-[var(--ui-text)]">WebDAV 归档</h3>
          <p class="text-xs text-[var(--ui-muted)]">备份短信、通话记录和录音，并清理已安全归档的本地数据</p>
        </div>
      </div>
      <el-switch v-model="form.enabled" :disabled="loading || busy" active-text="定时归档" />
    </div>

    <div v-if="loading" class="mt-6 text-sm text-[var(--ui-muted)]">正在加载归档设置…</div>
    <div v-else class="mt-6 grid min-w-0 gap-6 xl:grid-cols-[minmax(0,1.4fr)_minmax(280px,0.6fr)]">
      <div class="grid min-w-0 gap-4 sm:grid-cols-2">
        <label class="archive-field sm:col-span-2">
          <span>WebDAV 地址</span>
          <el-input v-model="form.endpoint" size="large" placeholder="https://dav.example.com/remote.php/dav/files/user" />
        </label>
        <label class="archive-field">
          <span>用户名</span>
          <el-input v-model="form.username" size="large" autocomplete="username" />
        </label>
        <div class="archive-field">
          <label for="archive-webdav-password">密码</label>
          <el-input
            id="archive-webdav-password"
            v-model="form.password"
            size="large"
            type="password"
            show-password
            autocomplete="new-password"
            :disabled="form.clear_password"
            :placeholder="passwordSet ? '留空保留现有密码' : '输入 WebDAV 密码'"
          />
          <el-checkbox v-if="passwordSet" v-model="form.clear_password">清除已保存的密码</el-checkbox>
        </div>
        <label class="archive-field sm:col-span-2">
          <span>远端目录</span>
          <el-input v-model="form.base_path" size="large" placeholder="hideck" />
        </label>
        <label class="archive-field">
          <span>归档周期（小时）</span>
          <el-input-number v-model="form.interval_hours" :min="1" :max="720" controls-position="right" class="!w-full" size="large" />
        </label>
        <label class="archive-field">
          <span>本地保留（天）</span>
          <el-input-number v-model="form.retention_days" :min="1" :max="3650" controls-position="right" class="!w-full" size="large" />
        </label>

        <fieldset class="archive-options sm:col-span-2">
          <legend>归档内容</legend>
          <el-checkbox v-model="form.include_sms">短信</el-checkbox>
          <el-checkbox v-model="form.include_calls">通话记录</el-checkbox>
          <el-checkbox v-model="form.include_recordings">通话录音</el-checkbox>
        </fieldset>

        <div class="archive-actions sm:col-span-2">
          <el-button size="large" :loading="testing" :disabled="busy || !form.endpoint" @click="testConnection">测试连接</el-button>
          <el-button size="large" type="primary" :loading="saving" :disabled="busy || !contentSelected" @click="saveSettings">
            <el-icon><Save24Regular /></el-icon>保存设置
          </el-button>
          <el-button size="large" :loading="running" :disabled="busy || !form.endpoint || hasUnsavedChanges" @click="runArchive">立即归档</el-button>
          <p v-if="hasUnsavedChanges" class="w-full text-xs text-[var(--ui-muted)]">保存当前设置后才能归档或预览清理。</p>
        </div>
      </div>

      <aside class="archive-side">
        <div class="archive-status" :class="{ 'archive-status-error': status.last_error }">
          <div class="flex items-center justify-between gap-3">
            <strong>{{ status.running ? '正在归档' : '最近一次归档' }}</strong>
            <span>{{ formatDate(status.last_completed_at) }}</span>
          </div>
          <p v-if="status.last_error" class="mt-2 break-words text-red-600 dark:text-red-300">{{ status.last_error }}</p>
          <p v-else class="mt-2 text-[var(--ui-muted)]">
            上传 {{ status.last_summary.uploaded }} 项，跳过 {{ status.last_summary.skipped }} 项，{{ formatBytes(status.last_summary.bytes) }}
          </p>
        </div>

        <div class="archive-cleanup">
          <div class="flex items-start justify-between gap-4">
            <div>
              <strong class="text-sm text-[var(--ui-text)]">本地清理</strong>
              <p class="mt-1 text-xs leading-5 text-[var(--ui-muted)]">只清理超过保留期且内容哈希已归档的数据。</p>
            </div>
            <label class="flex shrink-0 items-center gap-2 text-xs text-[var(--ui-muted)]">
              <span>归档后自动清理</span>
              <el-switch v-model="form.auto_cleanup" :disabled="busy" aria-label="归档成功后自动清理" />
            </label>
          </div>
          <el-button class="mt-4 !min-h-11 w-full" :loading="previewing" :disabled="busy || hasUnsavedChanges" @click="previewCleanup">预览清理范围</el-button>

          <div v-if="cleanupPreview" class="cleanup-preview" aria-live="polite">
            <dl>
              <div><dt>短信</dt><dd>{{ cleanupPreview.sms_count }}</dd></div>
              <div><dt>通话</dt><dd>{{ cleanupPreview.call_count }}</dd></div>
              <div><dt>录音</dt><dd>{{ cleanupPreview.recording_count }}</dd></div>
              <div><dt>录音空间</dt><dd>{{ formatBytes(cleanupPreview.recording_bytes) }}</dd></div>
            </dl>
            <el-button
              type="danger"
              plain
              class="mt-4 !min-h-11 w-full"
              :loading="cleaning"
              :disabled="busy || hasUnsavedChanges"
              @click="confirmCleanup"
            >
              <el-icon><Delete20Regular /></el-icon>确认清理
            </el-button>
          </div>
        </div>
      </aside>
    </div>
  </section>
</template>

<style scoped>
.archive-card { grid-column: 1 / -1; border-top: 1px solid var(--ui-border); }
.archive-icon { width: 48px; height: 48px; flex: none; display: grid; place-items: center; border-radius: var(--ui-radius-md); color: var(--ui-accent); background: var(--ui-selected); }
.archive-field { display: grid; min-width: 0; gap: 8px; color: var(--ui-text-muted); font-size: 12px; font-weight: 700; }
.archive-options { min-width: 0; padding: 14px 16px; border: 1px solid var(--ui-border); border-radius: var(--ui-radius-lg); }
.archive-options legend { padding: 0 6px; color: var(--ui-text-muted); font-size: 12px; font-weight: 700; }
.archive-actions { display: flex; flex-wrap: wrap; gap: 10px; }
.archive-actions :deep(.el-button) { min-height: 44px; margin-left: 0; }
.archive-side { display: grid; align-content: start; gap: 16px; min-width: 0; }
.archive-status, .archive-cleanup { padding: 16px; border: 1px solid var(--ui-border); border-radius: var(--ui-radius-lg); background: var(--ui-surface-muted); font-size: 13px; }
.archive-status-error { border-color: color-mix(in srgb, var(--el-color-danger) 42%, var(--ui-border)); }
.cleanup-preview { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--ui-border); }
.cleanup-preview dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.cleanup-preview dl div { padding: 10px; border-radius: var(--ui-radius-md); background: var(--el-bg-color); }
.cleanup-preview dt { color: var(--ui-muted); font-size: 11px; }
.cleanup-preview dd { margin-top: 4px; color: var(--ui-text); font-weight: 700; }
@media (max-width: 640px) {
  .archive-actions { display: grid; grid-template-columns: 1fr; }
  .archive-actions :deep(.el-button) { width: 100%; }
}
</style>
