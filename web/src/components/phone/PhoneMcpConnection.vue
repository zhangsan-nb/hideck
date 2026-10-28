<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { usePhoneStore } from '../../stores/phone'
import { mcpService, type McpToken } from '../../services/mcp'
import { errorMessage } from '../../services/http'
import { useMcpAudioBinding } from './useMcpAudioBinding'

const phone = usePhoneStore()
const open = ref(false)
const tokens = ref<McpToken[]>([])
const tokenId = ref('')
const name = ref('网页电话')
const binding = useMcpAudioBinding({ service: mcpService, enableMedia: () => phone.enableMedia(), media: () => phone })
const { connection } = binding
const busy = ref(false)
const error = ref('')
const status = computed(() => connection.value && phone.mediaReady ? 'MCP 音频已连接' : '连接 MCP')
let synchronization = Promise.resolve()

async function show() {
  open.value = true
  error.value = ''
  busy.value = true
  try {
    tokens.value = (await mcpService.tokens()).filter(token => !token.revoked_at)
    if (connection.value && !tokens.value.some(token => token.id === connection.value?.token_id)) {
      binding.forget()
      error.value = '授权令牌已撤销，请选择有效令牌重新连接。'
    }
    if (!tokens.value.some(token => token.id === tokenId.value)) tokenId.value = tokens.value[0]?.id || ''
  } catch (cause) { error.value = errorMessage(cause, '令牌加载失败') }
  finally { busy.value = false }
}
async function connect() {
  busy.value = true
  error.value = ''
  try {
    await binding.connect({ token_id: tokenId.value, name: name.value.trim() })
  } catch (cause) { error.value = errorMessage(cause, 'MCP 音频连接失败') }
  finally { busy.value = false }
}
async function disconnect() {
  if (!connection.value) return
  busy.value = true
  error.value = ''
  try { await binding.disconnect() }
  catch (cause) { error.value = errorMessage(cause, '解除 MCP 音频绑定失败') }
  finally { busy.value = false }
}
watch(() => [phone.mediaId, phone.lease, phone.mediaState], () => {
  synchronization = synchronization.then(binding.synchronize).catch(cause => { error.value = errorMessage(cause, 'MCP 音频授权更新失败') })
})
function pageHide() {
  void binding.pageHide().catch(cause => { console.error('MCP 音频绑定解除失败', errorMessage(cause, '请求失败')) })
}
window.addEventListener('pagehide', pageHide)
window.addEventListener('pageshow', binding.pageShow)
onBeforeUnmount(() => {
  window.removeEventListener('pagehide', pageHide)
  window.removeEventListener('pageshow', binding.pageShow)
  pageHide()
})
</script>

<template>
  <el-button :type="connection && phone.mediaReady ? 'primary' : 'default'" plain @click="show">{{ status }}</el-button>
  <el-dialog v-model="open" title="连接 MCP 网页音频" width="min(520px, 92vw)">
    <p class="mcp-audio-help">允许指定令牌使用当前网页的麦克风和听筒进行电话控制。请保持网页打开。</p>
    <p v-if="error" class="mcp-audio-error" role="alert">{{ error }}</p>
    <template v-if="connection">
      <p role="status">{{ phone.mediaReady ? '音频已授权' : '音频已中断，等待媒体恢复' }} · {{ connection.name }}</p>
      <p class="mcp-audio-id">连接 ID：<code>{{ connection.id }}</code></p>
      <p class="mcp-audio-help">解除授权不会额外挂断通话。</p>
      <el-button :loading="busy" type="danger" plain @click="disconnect">解除绑定</el-button>
    </template>
    <el-form v-else label-position="top" @submit.prevent="connect">
      <el-form-item label="授权令牌"><el-select v-model="tokenId" :disabled="busy" aria-label="授权的 MCP 令牌"><el-option v-for="token in tokens" :key="token.id" :value="token.id" :label="`${token.name} · ${token.prefix}…`" /></el-select></el-form-item>
      <el-form-item label="连接名称"><el-input v-model="name" :disabled="busy" aria-label="音频连接名称" /></el-form-item>
      <p v-if="!busy && !tokens.length" class="mcp-audio-help">请先在设置页开启 MCP 并创建令牌。</p>
      <el-button type="primary" native-type="submit" :loading="busy" :disabled="!tokenId || !name.trim()">连接并授权麦克风</el-button>
    </el-form>
  </el-dialog>
</template>

<style scoped>
.mcp-audio-help { color: var(--ui-text-muted); line-height: 1.6; margin-bottom: 16px; }
.mcp-audio-error { color: var(--el-color-danger); margin-bottom: 12px; }
.mcp-audio-id { overflow-wrap: anywhere; margin: 12px 0; }
.el-select { width: 100%; }
@media (max-width: 640px) { :deep(.el-button) { min-height: 44px; } }
</style>
