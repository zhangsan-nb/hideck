<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessageBox, type InputInstance } from 'element-plus'
import { mcpService, type McpToken } from '../../services/mcp'
import type { McpClientId } from '../../services/mcp-clients'
import { errorMessage } from '../../services/http'
import { formatDeviceDateTime } from '../../utils/deviceTime'
import { copyToClipboard } from '../../utils/clipboard'
import McpClientSetup from './McpClientSetup.vue'
import McpDiagnostics from './McpDiagnostics.vue'

const enabled = ref(false)
const loading = ref(true)
const saving = ref(false)
const creating = ref(false)
const revoking = ref('')
const deleting = ref('')
const error = ref('')
const name = ref('')
const nameInput = ref<InputInstance>()
const nameError = ref('')
const tokens = ref<McpToken[]>([])
const secret = ref('')
const showSecret = ref(false)
const selectedClient = ref<McpClientId>('codex')
const endpoint = computed(() => `${window.location.origin}/mcp`)
const diagnosticsKey = computed(() => JSON.stringify([enabled.value, tokens.value.map(token => [token.id, token.revoked_at])]))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [settings, list] = await Promise.all([mcpService.settings(), mcpService.tokens()])
    enabled.value = settings.enabled
    tokens.value = list
  } catch (cause) { error.value = errorMessage(cause, 'MCP 设置加载失败') }
  finally { loading.value = false }
}
async function toggle(value: string | number | boolean) {
  saving.value = true
  error.value = ''
  try { enabled.value = (await mcpService.configure(Boolean(value))).enabled }
  catch (cause) { error.value = errorMessage(cause, 'MCP 设置保存失败') }
  finally { saving.value = false }
}
async function create() {
  if (creating.value) return
  const tokenName = name.value.trim()
  if (!tokenName) {
    nameError.value = '请输入令牌名称。'
    nameInput.value?.focus()
    return
  }
  nameError.value = ''
  creating.value = true
  error.value = ''
  try {
    const created = await mcpService.createToken(tokenName)
    tokens.value = [created.token, ...tokens.value]
    secret.value = created.secret
    showSecret.value = true
    name.value = ''
  } catch (cause) { error.value = errorMessage(cause, '令牌创建失败') }
  finally { creating.value = false }
}
async function revoke(id: string) {
  revoking.value = id
  error.value = ''
  try {
    await mcpService.revokeToken(id)
    tokens.value = tokens.value.map(token => token.id === id ? { ...token, revoked_at: new Date().toISOString() } : token)
  } catch (cause) { error.value = errorMessage(cause, '令牌撤销失败') }
  finally { revoking.value = '' }
}
async function deleteToken(token: McpToken) {
  const message = token.revoked_at
    ? `删除“${token.name}”的记录？此操作不可恢复。`
    : `删除“${token.name}”？令牌将失效并解除音频授权，记录不可恢复。`
  try {
    await ElMessageBox.confirm(message, '删除 MCP 令牌', {
      confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning'
    })
  } catch (cause) {
    if (cause !== 'cancel' && cause !== 'close') error.value = errorMessage(cause, '无法确认删除令牌')
    return
  }
  deleting.value = token.id
  error.value = ''
  try {
    await mcpService.deleteToken(token.id)
    tokens.value = tokens.value.filter(item => item.id !== token.id)
  } catch (cause) { error.value = errorMessage(cause, '令牌删除失败') }
  finally { deleting.value = '' }
}
async function copy(value: string) {
  error.value = ''
  if (!await copyToClipboard(value)) error.value = '自动复制未完成，请手动选择并复制。'
}
onMounted(load)
</script>

<template>
  <section class="mcp-settings" aria-labelledby="mcp-settings-title" :aria-busy="loading">
    <div class="mcp-heading">
      <div><h3 id="mcp-settings-title">MCP 接入</h3><p>通过 AI 助手管理设备、短信和电话。</p></div>
      <el-switch :model-value="enabled" :loading="saving" :disabled="loading || saving" aria-label="启用 MCP 服务" @change="toggle" />
    </div>
    <p v-if="error" class="mcp-error" role="alert">{{ error }} <el-button v-if="!loading" text @click="load">重新加载</el-button></p>
    <p v-if="loading" role="status">正在加载 MCP 设置…</p>
    <template v-else>
      <label class="mcp-label" for="mcp-endpoint">服务地址 · {{ enabled ? '已开启' : '已关闭' }}</label>
      <div class="mcp-row"><el-input id="mcp-endpoint" :model-value="endpoint" readonly /><el-button @click="copy(endpoint)">复制地址</el-button></div>
      <p class="mcp-help">令牌拥有全部业务权限，可随时撤销。网页音频需在电话页授权。</p>
      <form @submit.prevent="create">
        <label class="mcp-label" for="mcp-token-name">令牌名称 <span class="mcp-required">（必填）</span></label>
        <div class="mcp-row">
          <el-input id="mcp-token-name" ref="nameInput" v-model="name" aria-label="新令牌名称" aria-required="true" :aria-invalid="!!nameError" :aria-describedby="nameError ? 'mcp-token-name-error' : undefined" placeholder="例如 Codex、Grok Build" :disabled="creating" @input="nameError = ''" />
          <el-button type="primary" native-type="submit" :loading="creating" :disabled="loading || creating">创建令牌</el-button>
        </div>
        <p v-if="nameError" id="mcp-token-name-error" class="mcp-name-error" role="alert">{{ nameError }}</p>
      </form>
      <ul class="mcp-token-list" aria-label="MCP 令牌">
        <li v-for="token in tokens" :key="token.id">
          <div><strong>{{ token.name }}</strong><code>{{ token.prefix }}…</code><small>{{ formatDeviceDateTime(token.created_at) }} · {{ token.revoked_at ? '已撤销' : '有效' }}</small></div>
          <div class="mcp-token-actions">
            <el-button v-if="!token.revoked_at" plain :loading="revoking === token.id" :disabled="!!revoking || !!deleting" :aria-label="`撤销 ${token.name}`" @click="revoke(token.id)">撤销</el-button>
            <el-button type="danger" plain :loading="deleting === token.id" :disabled="!!revoking || !!deleting" :aria-label="`删除 ${token.name}`" @click="deleteToken(token)">删除</el-button>
          </div>
        </li>
      </ul>
      <p v-if="!tokens.length" class="mcp-help">尚未创建令牌。</p>
      <McpClientSetup v-model="selectedClient" :endpoint="endpoint" />
      <McpDiagnostics :key="diagnosticsKey" />
    </template>
    <el-dialog v-model="showSecret" title="保存令牌" width="min(620px, 92vw)" append-to-body :close-on-click-modal="false" @closed="secret = ''">
      <p>令牌仅显示一次，请妥善保存。</p>
      <el-input :model-value="secret" readonly aria-label="新 MCP 令牌" />
      <div class="mcp-row mcp-secret-actions"><el-button @click="copy(secret)">复制令牌</el-button></div>
      <p v-if="error" class="mcp-error" role="alert">{{ error }}</p>
      <McpClientSetup v-model="selectedClient" :endpoint="endpoint" :token="secret || undefined" />
      <template #footer><el-button type="primary" @click="showSecret = false">已保存，关闭</el-button></template>
    </el-dialog>
  </section>
</template>

<style scoped>
.mcp-settings { grid-column: 1 / -1; min-width: 0; padding: 24px; border-top: 1px solid var(--ui-border); }
.mcp-heading, .mcp-row, .mcp-token-list li { display: flex; align-items: center; gap: 12px; }
.mcp-heading { justify-content: space-between; margin-bottom: 16px; }
.mcp-heading > div { min-width: 0; }
.mcp-heading :deep(.el-switch) { flex-shrink: 0; }
h3 { color: var(--ui-text); font-size: 18px; font-weight: 700; }
p, small { color: var(--ui-text-muted); }
.mcp-label { display: block; margin-bottom: 8px; font-weight: 600; }
.mcp-help { margin: 12px 0 16px; font-size: 13px; line-height: 1.6; }
.mcp-required { color: var(--ui-text-muted); font-size: 13px; font-weight: 400; }
.mcp-name-error { margin-top: 8px; color: var(--el-color-danger); font-size: 13px; line-height: 1.6; }
.mcp-error { color: var(--el-color-danger); margin-bottom: 12px; }
.mcp-token-list { margin: 16px 0; }
.mcp-token-list li { justify-content: space-between; padding: 12px 0; border-bottom: 1px solid var(--ui-border); }
.mcp-token-list li > div:first-child { display: grid; min-width: 0; gap: 4px; overflow-wrap: anywhere; }
.mcp-token-list .mcp-token-actions { display: flex; flex-shrink: 0; flex-wrap: wrap; gap: 8px; }
.mcp-token-actions :deep(.el-button + .el-button) { margin-left: 0; }
code { font-family: var(--font-mono, monospace); }
.mcp-secret-actions { margin-top: 12px; flex-wrap: wrap; }
@media (max-width: 640px) { .mcp-settings { padding: 16px; } .mcp-row { flex-wrap: wrap; } .mcp-row > .el-input { flex-basis: 100%; } .mcp-settings :deep(.el-button) { min-height: 44px; } }
</style>
