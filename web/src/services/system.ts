import { api } from '../stores/auth'
import { callService } from './http'
import type { DeviceTimeInfo } from '../utils/deviceTime'
import type { PasswordChangeResponse, PasswordCredentialStatus } from '../types/credentials'

export type DocsLinks = {
  swagger_ui: string
  openapi_yaml: string
  openapi_json: string
}

export type UpdateInfo = {
  has_update: boolean
  current_version: string
  latest_version: string
  release_note: string
  is_docker: boolean
}

export type SystemInfo = {
  version: string
  build_time: string
  config: string
  docs: DocsLinks
}

export type SystemSettings = {
	openwrt_dynamic_interfaces: boolean
}

export type ArchiveStatus = {
  running: boolean
  last_started_at?: string
  last_completed_at?: string
  last_error?: string
  last_summary: ArchiveRunSummary
}

export type ArchiveRunSummary = {
  uploaded: number
  skipped: number
  bytes: number
}

export type ArchiveSettings = {
  enabled: boolean
  endpoint: string
  username: string
  password_set: boolean
  base_path: string
  interval_hours: number
  retention_days: number
  auto_cleanup: boolean
  include_sms: boolean
  include_calls: boolean
  include_recordings: boolean
  status: ArchiveStatus
}

export type ArchiveSettingsPayload = Omit<ArchiveSettings, 'password_set' | 'status'> & {
  password: string
  clear_password?: boolean
}

export type ArchiveCleanupPreview = {
	token: string
  before: string
  sms_count: number
  call_count: number
  recording_count: number
  recording_bytes: number
}

export type AutoAnswerSettings = {
  audio_ready: boolean
  audio_name: string
  devices: AutoAnswerDeviceSettings[]
}

export type AutoAnswerDeviceSettings = {
  device_id: string
  enabled: boolean
  delay_seconds: number
}

export type PhoneSettings = {
  auto_answer: AutoAnswerSettings
}

export type DisclaimerStatus = {
  accepted: boolean
  accepted_at: string | null
  version: string
}

export type TelegramRecordingMode = 'voice' | 'audio'

export type TelegramSettings = {
  enabled: boolean
  bot_token: string
  chat_id: number | null
  admin_id: number | null
  bound_chat_id: number | null
  binding_error: string
  recording_mode: TelegramRecordingMode
  base_url: string
  proxy: string
}

export type FeishuSettings = {
  enabled: boolean
  app_id: string
  app_secret: string
  chat_ids: string[]
}

export type QQSettings = {
  enabled: boolean
  app_id: string
  app_secret: string
  group_ids: string
  direct_ids: string
}

export type WeixinSettings = {
  enabled: boolean
  base_url: string
  allowed_user_ids: string[]
  allowed_group_ids: string[]
}

export type WeComBotSettings = {
  enabled: boolean
  bot_id: string
  secret: string
  websocket_url: string
  allowed_user_ids: string[]
  allowed_group_ids: string[]
}

export type WebhookSettings = {
  enabled: boolean
  urls: string[]
  secret: string
  timeout_ms: number
  retry_max: number
  text_template: string
  payload_template: string
  headers: Record<string, string>
}

export type BarkSettings = {
  enabled: boolean
  urls: string[]
  group: string
  icon: string
  level: string
}

export type EmailSettings = {
  enabled: boolean
  use_ssl: boolean
  smtp_host: string
  smtp_port: number
  username: string
  password: string
  from_address: string
  to_addresses: string[]
}

export type PushplusSettings = {
  enabled: boolean
  token: string
  topic: string
  channel: string
}

export type WeComSettings = {
  enabled: boolean
  urls: string[]
  payload_template: string
}

export type NotificationsSettingsResponse = {
  telegram?: Partial<TelegramSettings>
  feishu?: Partial<FeishuSettings>
  qq?: Partial<QQSettings>
  weixin?: Partial<WeixinSettings>
  wecom_bot?: Partial<WeComBotSettings>
  email?: Partial<EmailSettings>
  pushplus?: Partial<PushplusSettings>
  webhook?: Partial<WebhookSettings>
  bark?: Partial<BarkSettings>
  wecom?: Partial<WeComSettings>
  device_filters?: Record<string, string[]>
}

export type SaveNotificationsPayload = {
  device_filters?: Record<string, string[]>
  telegram: {
    enabled: boolean
    bot_token: string
    chat_id: number
    admin_id: number
    recording_mode: TelegramRecordingMode
    base_url: string
    proxy: string
  }
  feishu: {
    enabled: boolean
    app_id: string
    app_secret: string
    chat_ids: string[]
  }
  qq: {
    enabled: boolean
    app_id: string
    app_secret: string
    group_ids: string
    direct_ids: string
  }
  weixin: WeixinSettings
  wecom_bot: WeComBotSettings
  email: {
    enabled: boolean
    use_ssl: boolean
    smtp_host: string
    smtp_port: number
    username: string
    password: string
    from_address: string
    to_addresses: string[]
  }
  pushplus: {
    enabled: boolean
    token: string
    topic: string
    channel: string
  }
  webhook: {
    enabled: boolean
    urls: string[]
    secret: string
    timeout_ms: number
    retry_max: number
    text_template: string
    payload_template: string
    headers?: Record<string, string>
  }
  bark: {
    enabled: boolean
    urls: string[]
    group: string
    icon: string
    level: string
  }
  wecom: WeComSettings
}

export type SaveNotificationsResponse = {
  applied?: boolean
  warning?: string
}

export type TestWebhookPayload = {
  enabled: boolean
  urls: string[]
  secret: string
  timeout_ms: number
  retry_max: number
  text_template: string
  payload_template: string
  headers?: Record<string, string>
}

export type TestWebhookResponse = {
  ok: boolean
  message: string
  failed_urls?: string[]
}

export type TestBarkPayload = {
  enabled: boolean
  urls: string[]
  group: string
  icon: string
  level: string
}

export type TestBarkResponse = {
  ok: boolean
  message: string
  failed_urls?: string[]
}

export type TestEmailPayload = {
  enabled: boolean
  use_ssl: boolean
  smtp_host: string
  smtp_port: number
  username: string
  password: string
  from_address: string
  to_addresses: string[]
}

export type TestEmailResponse = {
  ok: boolean
  message: string
}

export type TestWeComPayload = WeComSettings

export type TestWeComResponse = {
  ok: boolean
  message: string
  failed_count?: number
}

export const systemService = {
  getDisclaimerStatus() {
    return callService(async () => {
      const res = await api.get<DisclaimerStatus>('/settings/disclaimer')
      return res.data
    })
  },
  acceptDisclaimer(confirmation: string) {
    return callService(async () => {
      const res = await api.put<DisclaimerStatus>('/settings/disclaimer', { confirmation })
      return res.data
    })
  },
  getTime() {
    return callService(async () => {
      const res = await api.get('/system/time')
      return res.data as DeviceTimeInfo
    })
  },
  getInfo() {
    return callService(async () => {
      const res = await api.get('/system/info')
      return res.data as SystemInfo
    })
  },
  getSystemSettings() {
    return callService(async () => {
      const res = await api.get<SystemSettings>('/settings/system')
      return res.data
    })
  },
  saveSystemSettings(payload: SystemSettings) {
    return callService(async () => {
      await api.put('/settings/system', payload)
      return true
    })
  },
  getArchiveSettings() {
    return callService(async () => {
      const res = await api.get<ArchiveSettings>('/settings/archive')
      return res.data
    })
  },
  saveArchiveSettings(payload: ArchiveSettingsPayload) {
    return callService(async () => {
      const res = await api.put<ArchiveSettings>('/settings/archive', payload)
      return res.data
    })
  },
  testArchiveSettings(payload: ArchiveSettingsPayload) {
    return callService(async () => {
      await api.post('/settings/archive/test', payload)
      return true
    })
  },
  runArchive() {
    return callService(async () => {
      const res = await api.post<{ status: string; summary: ArchiveRunSummary }>('/archive/actions/run', {})
      return res.data.summary
    })
  },
  previewArchiveCleanup() {
    return callService(async () => {
      const res = await api.get<ArchiveCleanupPreview>('/archive/cleanup/preview')
      return res.data
    })
  },
  cleanupArchive(previewToken: string) {
    return callService(async () => {
      const res = await api.post<{ status: string; cleaned: ArchiveCleanupPreview }>('/archive/cleanup', {
        confirm: true,
        preview_token: previewToken,
      })
      return res.data.cleaned
    })
  },
  getPhoneSettings() {
    return callService(async () => {
      const res = await api.get<PhoneSettings>('/settings/phone')
      return res.data
    })
  },
  saveAutoAnswerDevice(deviceId: string, payload: Pick<AutoAnswerDeviceSettings, 'enabled' | 'delay_seconds'>) {
    return callService(async () => {
      await api.put(`/settings/phone/auto-answer/devices/${encodeURIComponent(deviceId)}`, payload)
      return true
    })
  },
  uploadAutoAnswerAudio(file: File) {
    return callService(async () => {
      const data = new FormData()
      data.append('file', file)
      const res = await api.post<AutoAnswerSettings>('/settings/phone/auto-answer/audio', data)
      return res.data
    })
  },
  getPasswordStatus() {
    return callService(async () => {
      const res = await api.get<PasswordCredentialStatus>('/settings/password')
      return res.data
    })
  },
  changePassword(payload: { old_password: string; new_password: string; confirm_password: string }) {
    return callService(async () => {
      const res = await api.post<PasswordChangeResponse>('/settings/password', payload)
      return res.data
    })
  },
  getNotifications() {
    return callService(async () => {
      const res = await api.get('/settings/notifications')
      return (res.data || {}) as NotificationsSettingsResponse
    })
  },
  saveNotifications(payload: SaveNotificationsPayload) {
    return callService(async () => {
      const res = await api.put<SaveNotificationsResponse>('/settings/notifications', payload)
      return {
        applied: res.data?.applied,
        warning: res.data?.warning
      }
    })
  },
  testWebhook(payload: TestWebhookPayload) {
    return callService(async () => {
      const res = await api.post<TestWebhookResponse>('/settings/notifications/webhook/test', payload)
      return res.data
    })
  },
  testBark(payload: TestBarkPayload) {
    return callService(async () => {
      const res = await api.post<TestBarkResponse>('/settings/notifications/bark/test', payload)
      return res.data
    })
  },
  testEmail(payload: TestEmailPayload) {
    return callService(async () => {
      const res = await api.post<TestEmailResponse>('/settings/notifications/email/test', payload)
      return res.data
    })
  },
  testWeCom(payload: TestWeComPayload) {
    return callService(async () => {
      const res = await api.post<TestWeComResponse>('/settings/notifications/wecom/test', payload)
      return res.data
    })
  },
  checkUpdate() {
    return callService(async () => {
      const res = await api.get<UpdateInfo>('/system/update/check')
      return res.data
    })
  },
  applyUpdate() {
    return callService(async () => {
      const res = await api.post<{ message: string }>('/system/update/apply', {})
      return res.data
    })
  }
}
