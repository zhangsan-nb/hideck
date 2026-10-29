import type { VoWiFiRuntimeState } from '../types/api'

// Port-s closed or timed out while registration is up; the P-CSCF reopens it
// before its next request (TS 33.203 7.1), so this is not a failure yet.
const portSAwaitingPeerReason = 'IMS registered; port-s closed or timed out, awaiting the P-CSCF reconnect'

function portSAwaitingPeer(runtime?: VoWiFiRuntimeState): boolean {
  return runtime?.sms_ready_reason === portSAwaitingPeerReason
}

export function smsReceptionPending(runtime?: VoWiFiRuntimeState): boolean {
  return runtime?.ims_ready === true && runtime.sms_ready !== true &&
    (runtime.sms_rx_pending === true || portSAwaitingPeer(runtime))
}

export function smsReceptionDetail(runtime?: VoWiFiRuntimeState): string {
  if (smsReceptionPending(runtime)) {
    return portSAwaitingPeer(runtime) ? '端口已断，等运营商连入' : 'IMS 已注册，短信接收待验证'
  }
  if (runtime?.sms_ready && runtime.sms_ready_reason === 'IMS SMS receiver verified on registered TCP flow') {
    return '短信接收已验证，复用注册连接'
  }
  if (runtime?.sms_ready && (runtime.sms_ready_reason === 'IMS SMS receiver ready; port-s timed out, P-CSCF reconnects on demand' ||
    runtime.sms_ready_reason === 'IMS SMS receiver ready; port-s timed out, REGISTER probing the P-CSCF')) {
    return '端口已断，等运营商按需连入'
  }
  return ''
}
