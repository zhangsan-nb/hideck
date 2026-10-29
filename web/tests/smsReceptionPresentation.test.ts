import assert from 'node:assert/strict'
import test from 'node:test'
import { createDashboardStages } from '../src/utils/dashboardPresentation.ts'
import { smsReceptionDetail, smsReceptionPending } from '../src/utils/smsReceptionPresentation.ts'

test('unverified SMS RX is neutral, while SMS TX remains usable', () => {
  const runtime = { ims_ready: true, sms_ready: false, sms_mo_ready: true, sms_rx_pending: true }
  const stages = createDashboardStages(runtime)
  assert.deepEqual(stages[4], { key: 'SMS TX', ready: true })
  assert.deepEqual(stages[5], { key: 'SMS RX', ready: undefined, pending: true })
  assert.equal(smsReceptionDetail(runtime), 'IMS 已注册，短信接收待验证')
  assert.equal(smsReceptionPending({ ...runtime, ims_ready: false }), false)
})

test('an explicit reception failure stays failed', () => {
  const runtime = { ims_ready: true, sms_ready: false, sms_rx_pending: false }
  assert.equal(createDashboardStages(runtime)[5]?.ready, false)
  assert.equal(smsReceptionDetail(runtime), '')
})

test('a closed or timed-out port-s is neutral until the P-CSCF reconnects', () => {
  const runtime = { ims_ready: true, sms_ready: false, sms_mo_ready: true, sms_rx_pending: false,
    sms_ready_reason: 'IMS registered; port-s closed or timed out, awaiting the P-CSCF reconnect' }
  assert.deepEqual(createDashboardStages(runtime)[5], { key: 'SMS RX', ready: undefined, pending: true })
  assert.equal(smsReceptionDetail(runtime), '端口已断，等运营商连入')
  assert.equal(smsReceptionPending({ ...runtime, ims_ready: false }), false)
  assert.equal(createDashboardStages({ ...runtime, sms_ready_reason: 'IMS SMS receiver is not ready' })[5]?.ready, false)
})

test('verified registered-flow reception describes its actual route', () => {
  const runtime = { ims_ready: true, sms_ready: true, sms_rx_pending: false,
    sms_ready_reason: 'IMS SMS receiver verified on registered TCP flow' }
  assert.equal(createDashboardStages(runtime)[5]?.ready, true)
  assert.equal(smsReceptionDetail(runtime), '短信接收已验证，复用注册连接')
})

test('reception awaiting an on-demand port-s says so', () => {
  const runtime = { ims_ready: true, sms_ready: true, sms_rx_pending: false,
    sms_ready_reason: 'IMS SMS receiver ready; port-s timed out, P-CSCF reconnects on demand' }
  assert.equal(createDashboardStages(runtime)[5]?.ready, true)
  assert.equal(smsReceptionDetail(runtime), '端口已断，等运营商按需连入')
  const probing = { ...runtime, sms_ready_reason: 'IMS SMS receiver ready; port-s timed out, REGISTER probing the P-CSCF' }
  assert.equal(createDashboardStages(probing)[5]?.ready, true)
  assert.equal(smsReceptionDetail(probing), '端口已断，等运营商按需连入')
})
