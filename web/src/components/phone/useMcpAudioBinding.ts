import { readonly, ref } from 'vue'
import type { McpConnection, mcpService } from '../../services/mcp'

type BrowserMedia = { mediaId: string; lease: string; mediaReady: boolean; mediaState: string }
type Dependencies = {
  service: Pick<typeof mcpService, 'bind' | 'disconnect' | 'disconnectOnPageHide'>
  enableMedia: () => Promise<unknown>
  media: () => BrowserMedia
}

export function useMcpAudioBinding(dependencies: Dependencies) {
  const connection = ref<McpConnection | null>(null)
  let generation = 0
  let pageActive = true

  function forget() {
    generation++
    connection.value = null
  }

  async function connect(input: { token_id: string; name: string }) {
    const started = generation
    await dependencies.enableMedia()
    if (!pageActive || started !== generation) throw new Error('页面已离开，MCP 音频授权已取消')
    const media = dependencies.media()
    const bound = await dependencies.service.bind({ ...input, media_id: media.mediaId, lease: media.lease })
    if (!pageActive || started !== generation) {
      await dependencies.service.disconnect(bound.id)
      throw new Error('页面已离开，MCP 音频授权已取消')
    }
    connection.value = bound
  }

  async function disconnect() {
    const current = connection.value
    if (!current) return
    const started = ++generation
    await dependencies.service.disconnect(current.id)
    if (started === generation) connection.value = null
  }

  async function synchronize() {
    const current = connection.value
    if (!current || !pageActive) return
    const media = dependencies.media()
    if (!media.mediaId || media.mediaState === 'idle' || media.mediaState === 'failed') {
      await disconnect()
      return
    }
    if (!media.mediaReady) return
    const started = generation
    const updated = await dependencies.service.bind({
      token_id: current.token_id, name: current.name, media_id: media.mediaId, lease: media.lease
    }, current.id)
    if (pageActive && started === generation && connection.value?.id === current.id) connection.value = updated
  }

  async function pageHide() {
    pageActive = false
    const current = connection.value
    forget()
    if (current) await dependencies.service.disconnectOnPageHide(current.id)
  }

  function pageShow() { pageActive = true }

  return { connection: readonly(connection), connect, disconnect, synchronize, forget, pageHide, pageShow }
}
