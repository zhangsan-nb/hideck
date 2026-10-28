import { ref, watch, type Ref } from 'vue'
import type { CardPolicy } from '../types/api'

type EditablePolicyFields = Pick<CardPolicy, 'ip_version' | 'apn' | 'vowifi_upstream_proxy_id' | 'local_phone_override'>
type SaveResult = { ok: boolean; error?: { message?: string; code?: string } }

export const CARD_POLICY_SAVED_RESTART_FAILED = 'card_policy_saved_restart_failed'

export function useCardPolicyFields(
  source: Ref<CardPolicy | null>,
  save: (patch: Partial<EditablePolicyFields>) => Promise<SaveResult>,
  onChanged?: () => void
) {
  const ipVersion = ref<CardPolicy['ip_version']>('v4')
  const apn = ref('')
  const vowifiUpstreamProxyID = ref('')
  const localPhoneOverride = ref('')
  const pending = ref<keyof EditablePolicyFields | null>(null)
  const error = ref('')
  const errorCode = ref('')
  const errorField = ref<keyof EditablePolicyFields | null>(null)
  const persisted = ref<Record<keyof EditablePolicyFields, string>>({
    ip_version: 'v4', apn: '', vowifi_upstream_proxy_id: '', local_phone_override: ''
  })

  watch(source, (policy) => {
    if (!policy || pending.value) return
    ipVersion.value = policy.ip_version || 'v4'
    apn.value = policy.apn || ''
    vowifiUpstreamProxyID.value = policy.vowifi_upstream_proxy_id || ''
    localPhoneOverride.value = policy.local_phone_override || ''
    persisted.value = {
      ip_version: ipVersion.value,
      apn: apn.value,
      vowifi_upstream_proxy_id: vowifiUpstreamProxyID.value,
      local_phone_override: localPhoneOverride.value
    }
  }, { immediate: true })

  function currentValue(field: keyof EditablePolicyFields) {
    if (field === 'ip_version') return ipVersion.value
    if (field === 'apn') return apn.value
    if (field === 'vowifi_upstream_proxy_id') return vowifiUpstreamProxyID.value
    return localPhoneOverride.value
  }

  function restore(field: keyof EditablePolicyFields) {
    const value = persisted.value[field]
    if (field === 'ip_version') ipVersion.value = value as CardPolicy['ip_version']
    else if (field === 'apn') apn.value = value
    else if (field === 'vowifi_upstream_proxy_id') vowifiUpstreamProxyID.value = value
    else localPhoneOverride.value = value
  }

  async function persist(field: keyof EditablePolicyFields) {
    if (!source.value || pending.value) return
    const previous = persisted.value[field]
    if (field === 'apn') apn.value = apn.value.trim()
    if (field === 'local_phone_override') localPhoneOverride.value = localPhoneOverride.value.trim()
    const value = currentValue(field)
    if (value === previous) return

    pending.value = field
    error.value = ''
    errorCode.value = ''
    errorField.value = null
    const result = await save({ [field]: value })
    pending.value = null
    if (!result.ok) {
      const code = result.error?.code || ''
      if (code === CARD_POLICY_SAVED_RESTART_FAILED) onChanged?.()
      else restore(field)
      error.value = result.error?.message || '策略保存失败'
      errorCode.value = code
      errorField.value = field
      return
    }
    persisted.value = { ...persisted.value, [field]: value }
    onChanged?.()
  }

  return {
    ipVersion, apn, vowifiUpstreamProxyID, localPhoneOverride, pending, error, errorCode, errorField,
    saveIPVersion: () => persist('ip_version'),
    saveAPN: () => persist('apn'),
    saveVowifiUpstreamProxy: () => persist('vowifi_upstream_proxy_id'),
    saveLocalPhoneOverride: () => persist('local_phone_override')
  }
}
