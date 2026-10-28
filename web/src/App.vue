<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from './stores/auth'
import LoadingScreen from './components/LoadingScreen.vue'
import ErrorState from './components/ErrorState.vue'
import { ElMessage } from 'element-plus'
import { systemService } from './services/system'
import { configureDeviceTime, resetDeviceTime } from './utils/deviceTime'
import {
  canRenderShell as resolveCanRenderShell,
  canShowDisclaimer,
  type StartupState
} from './utils/startupGate'
import { Warning24Regular } from '@vicons/fluent'
import { useTheme } from './composables/useTheme'
import { locale, t, useLocale } from './i18n'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import enLocale from 'element-plus/es/locale/lang/en'

const route = useRoute()
const auth = useAuthStore()
const { isDark, toggleTheme } = useTheme()
const disclaimerAccepted = ref(false)
const confirmText = ref('')
useLocale()
const expectedConfirmText = computed(() => t('disclaimer.phrase'))
const elLocale = computed(() => (locale.value === 'en' ? enLocale : zhCn))
const acceptingDisclaimer = ref(false)
const disclaimerActionError = ref('')
const canAccept = computed(() => confirmText.value === expectedConfirmText.value && !acceptingDisclaimer.value)
const deviceTimeState = ref<StartupState>(auth.isAuthenticated ? 'loading' : 'idle')
const deviceTimeError = ref('')
const disclaimerState = ref<StartupState>(auth.isAuthenticated ? 'loading' : 'idle')
const disclaimerError = ref('')
let deviceTimeGeneration = 0
let disclaimerGeneration = 0

async function syncDeviceTime() {
  const generation = ++deviceTimeGeneration
  deviceTimeState.value = 'loading'
  deviceTimeError.value = ''
  const requestStartedAt = Date.now()
  const result = await systemService.getTime()
  const responseReceivedAt = Date.now()
  if (generation !== deviceTimeGeneration || !auth.isAuthenticated) return
  if (!result.ok) {
    deviceTimeState.value = 'error'
    deviceTimeError.value = result.error.message
    return
  }
  try {
    configureDeviceTime(result.data, requestStartedAt, responseReceivedAt)
    deviceTimeState.value = 'ready'
  } catch (error) {
    deviceTimeState.value = 'error'
    deviceTimeError.value = error instanceof Error ? error.message : '设备时间同步失败'
  }
}

async function loadDisclaimerStatus() {
  const generation = ++disclaimerGeneration
  disclaimerState.value = 'loading'
  disclaimerError.value = ''
  disclaimerActionError.value = ''
  const result = await systemService.getDisclaimerStatus()
  if (generation !== disclaimerGeneration || !auth.isAuthenticated) return
  if (!result.ok) {
    disclaimerState.value = 'error'
    disclaimerError.value = result.error.message
    return
  }
  confirmText.value = ''
  disclaimerAccepted.value = result.data.accepted
  disclaimerState.value = 'ready'
}

async function acceptDisclaimer() {
  if (!canAccept.value) return
  acceptingDisclaimer.value = true
  disclaimerActionError.value = ''
  const result = await systemService.acceptDisclaimer(confirmText.value)
  acceptingDisclaimer.value = false
  if (!result.ok) {
    disclaimerActionError.value = result.error.message
    ElMessage.error(result.error.message)
    return
  }
  confirmText.value = ''
  disclaimerAccepted.value = true
}

watch(() => auth.isAuthenticated, (isAuthenticated) => {
  if (isAuthenticated) {
    void syncDeviceTime()
    void loadDisclaimerStatus()
    return
  }
  deviceTimeGeneration++
  disclaimerGeneration++
  resetDeviceTime()
  deviceTimeState.value = 'idle'
  disclaimerState.value = 'idle'
  deviceTimeError.value = ''
  disclaimerError.value = ''
  disclaimerActionError.value = ''
  disclaimerAccepted.value = false
}, { immediate: true })

function rejectDisclaimer() {
  ElMessage.warning(t('disclaimer.uninstalling'))
  const token = localStorage.getItem('token') || ''
  fetch('/api/system/uninstall', {
    method: 'POST',
    headers: token ? { Authorization: `Bearer ${token}` } : undefined
  })
    .finally(() => {
      document.body.innerHTML = `<div style="display:flex;height:100vh;background:var(--ui-bg);align-items:center;justify-content:center;font-size:24px;color:var(--ui-danger);font-weight:bold;font-family:sans-serif;flex-direction:column;gap:16px;"><div><svg style="width:64px;height:64px;" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" /></svg></div><div>${t('disclaimer.uninstalled')}</div></div>`
    })
}

const AuthenticatedShell = defineAsyncComponent(() => import('./layouts/AuthenticatedShell.vue'))
const UnauthenticatedShell = defineAsyncComponent(() => import('./layouts/UnauthenticatedShell.vue'))
const shell = computed(() =>
  auth.isAuthenticated && route.name !== 'Login' ? AuthenticatedShell : UnauthenticatedShell
)
const startupGateState = computed(() => ({
  isAuthenticated: auth.isAuthenticated,
  deviceTimeState: deviceTimeState.value,
  disclaimerState: disclaimerState.value
}))
const canRenderShell = computed(() => resolveCanRenderShell(startupGateState.value))
const showDisclaimer = computed(() => canShowDisclaimer({
  ...startupGateState.value,
  accepted: disclaimerAccepted.value
}))
const startupLoading = computed(() => auth.isAuthenticated && (
  deviceTimeState.value === 'loading' || disclaimerState.value === 'loading'
))
const startupError = computed(() => {
  if (deviceTimeState.value === 'error') {
    return { title: t('disclaimer.timeFailed'), message: deviceTimeError.value }
  }
  return { title: t('disclaimer.disclaimerFailed'), message: disclaimerError.value }
})

function retryStartup() {
  if (deviceTimeState.value === 'error') void syncDeviceTime()
  if (disclaimerState.value === 'error') void loadDisclaimerStatus()
}
</script>

<template>
  <el-config-provider :locale="elLocale">
  <div class="app-root h-screen w-screen overflow-hidden font-sans transition-colors duration-300">
    <Suspense v-if="canRenderShell">
      <template #default>
        <component :is="shell" :is-dark="isDark" @toggle-theme="toggleTheme" />
      </template>
      <template #fallback>
        <LoadingScreen />
      </template>
    </Suspense>
    <LoadingScreen v-else-if="startupLoading" />
    <div v-else class="h-full flex items-center justify-center p-6">
      <ErrorState
        class="w-full max-w-xl"
        :title="startupError.title"
        :message="startupError.message"
        :retry-text="t('common.retry')"
        @retry="retryStartup"
      />
    </div>

    <!-- 高级感全屏免责声明弹窗 -->
    <Transition name="fade-slide">
      <div v-if="showDisclaimer" class="license-overlay fixed inset-0 z-[9999] flex items-center justify-center">
        <div class="license-dialog relative w-full max-w-lg p-7 mx-4 overflow-hidden">
          <div class="relative z-10">
            <div class="license-icon flex items-center justify-center w-12 h-12 mx-auto mb-5">
              <Warning24Regular class="w-6 h-6" />
            </div>
            
            <h2 class="mb-5 text-2xl font-extrabold text-center text-[var(--ui-text)] tracking-tight">{{ t('disclaimer.title') }}</h2>
            
            <div class="space-y-4 text-[14px] text-[var(--ui-muted)] leading-relaxed font-medium">
              <div class="flex items-start">
                <div class="license-index">1</div>
                <p>{{ t('disclaimer.p1') }}</p>
              </div>
              <div class="flex items-start">
                <div class="license-index">2</div>
                <p>{{ t('disclaimer.p2') }}</p>
              </div>
              <div class="flex items-start">
                <div class="license-index">3</div>
                <p>{{ t('disclaimer.p3') }}</p>
              </div>
              <div class="flex items-start">
                <div class="license-index">4</div>
                <p>{{ t('disclaimer.p4') }}</p>
              </div>
            </div>
            
            <div class="mt-6 pt-5 border-t border-[var(--ui-border)]">
              <p class="mb-3 text-xs font-bold text-center text-[var(--ui-muted)]">
                {{ t('disclaimer.typeHint', { phrase: expectedConfirmText }) }}
              </p>
              
              <div class="mb-5">
                <el-input
                  v-model="confirmText"
                  class="license-input w-full px-4 py-3 text-center text-sm font-semibold outline-none transition-all"
                  :placeholder="t('disclaimer.placeholder', { phrase: expectedConfirmText })"
                  @paste.prevent
                  autocomplete="off"
                />
              </div>

              <p v-if="disclaimerActionError" class="mb-4 text-center text-sm text-red-500 dark:text-red-400">
                {{ disclaimerActionError }}
              </p>

              <div class="flex gap-4">
                <el-button class="license-reject flex-1 px-4 py-3 text-sm font-bold transition-colors" @click="rejectDisclaimer">
                  {{ t('disclaimer.reject') }}
                </el-button>
                <el-button
                  type="primary"
                  @click="acceptDisclaimer" 
                  :disabled="!canAccept"
                  :class="[
                    'flex-[1.5] px-4 py-3 text-sm font-bold tracking-wide transition-all duration-300 rounded-[var(--ui-radius-pill)]',
                    canAccept 
                      ? 'license-accept cursor-pointer'
                      : 'license-accept license-accept-disabled cursor-not-allowed'
                  ]"
                >
                  {{ acceptingDisclaimer ? t('common.loading') : t('disclaimer.accept') }}
                </el-button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </div>
  </el-config-provider>
</template>

<style>
.app-root {
  background: var(--ui-bg);
  color: var(--ui-text);
}

.license-overlay {
  padding: 16px;
  background: color-mix(in srgb, var(--ui-primary-solid) 56%, transparent);
}

.license-dialog {
  border: 1px solid var(--ui-border);
  border-radius: var(--ui-radius-lg);
  background: var(--ui-surface-strong);
  box-shadow: var(--ui-shadow-lg);
}

.license-dialog h2 {
  color: var(--ui-text);
  background: none;
  -webkit-text-fill-color: currentColor;
  letter-spacing: 0;
}

.license-icon {
  border: 1px solid color-mix(in srgb, var(--ui-warning) 34%, var(--ui-border));
  border-radius: var(--ui-radius-md);
  background: color-mix(in srgb, var(--ui-warning) 12%, var(--ui-surface));
  color: var(--ui-warning);
}

.license-index {
  width: 24px;
  height: 24px;
  margin: 2px 12px 0 0;
  flex: 0 0 24px;
  display: grid;
  place-items: center;
  border-radius: var(--ui-radius-sm);
  background: color-mix(in srgb, var(--ui-primary) 12%, var(--ui-surface));
  color: var(--ui-primary);
  font-family: "v-mono", ui-monospace, monospace;
  font-size: var(--ui-font-caption);
  font-weight: 700;
}

.license-emphasis {
  color: var(--ui-primary);
}

.license-input {
  padding: 0;
}

.license-input .el-input__wrapper {
  min-height: 44px;
  border-radius: var(--ui-radius-md);
  background: var(--ui-surface-subtle);
  box-shadow: 0 0 0 1px var(--ui-border) inset;
}

.license-input .el-input__wrapper.is-focus {
  box-shadow: var(--ui-focus);
}

.license-input .el-input__inner {
  color: var(--ui-text);
  text-align: center;
}

.license-reject,
.license-accept {
  min-height: 44px;
  margin-left: 0;
  border: 1px solid var(--ui-border);
  border-radius: var(--ui-radius-pill);
}

.license-reject {
  background: var(--ui-surface-muted);
  color: var(--ui-text-muted);
}

.license-reject:hover {
  border-color: var(--ui-danger);
  color: var(--ui-danger);
}

.license-accept {
  border-color: var(--ui-primary);
  background: var(--ui-primary-solid);
  color: #fff;
}

.license-accept:hover {
  background: var(--ui-primary-hover);
}

.license-accept-disabled {
  border-color: var(--ui-border);
  background: var(--ui-surface-muted);
  color: var(--ui-text-muted);
  opacity: .58;
}

.fade-slide-enter-active,
.fade-slide-leave-active {
  transition: opacity 180ms ease, transform 180ms ease;
}

.fade-slide-enter-from {
  opacity: 0;
  transform: translateY(20px);
}

.fade-slide-leave-to {
  opacity: 0;
  transform: translateY(-20px);
}

/* Custom Scrollbar */
::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
::-webkit-scrollbar-track {
  background: transparent;
}
::-webkit-scrollbar-thumb {
  background: color-mix(in srgb, var(--ui-text-muted) 55%, var(--ui-border));
  border-radius: 4px;
}
::-webkit-scrollbar-thumb:hover {
  background: color-mix(in srgb, var(--ui-text-muted) 75%, var(--ui-border));
}
</style>
