<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Add24Regular,
  ArrowClockwise24Regular,
  ArrowDownload24Regular,
  ArrowUpload24Regular,
  CheckboxChecked24Regular,
  Delete24Regular,
  Dismiss24Regular,
  Person24Regular,
  PersonEdit24Regular,
  Search24Regular,
  SelectAllOn24Regular
} from '@vicons/fluent'
import { usePhoneIdentity } from '../composables/usePhoneIdentity'
import { useContactImport } from '../composables/useContactImport'
import { phoneContactsService, type PhoneIdentity } from '../services/phone-contacts'
import {
  isPhoneContactNumberValid,
  normalizePhoneContactNumber,
  phoneContactNumberError,
  validPhoneContactNumbers
} from '../utils/phoneContactDraft'

const props = defineProps<{
  modelValue: boolean
  deviceId?: string
  kind?: 'dial' | 'sms'
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  select: [number: string]
}>()

type ContactGroup = {
  key: string
  contactId?: string
  name: string
  items: PhoneIdentity[]
}

const CONTACT_SCROLL_THRESHOLD = 160
const CONTACT_SEARCH_DELAY_MS = 250
const MANUAL_CONTACT_GROUP_KEY = 'manual-contact'

const identities = usePhoneIdentity()
const query = ref('')
const adding = ref(false)
const selecting = ref(false)
const expanded = ref('')
const selected = ref<string[]>([])
const searchInput = ref<{ focus?: () => void } | null>(null)
const nameInput = ref<{ focus?: () => void } | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const draftName = ref('')
const draftNumber = ref('')
const extraNumbers = ref<string[]>([])
const saving = ref(false)
const exporting = ref(false)
const selectingAll = ref(false)

const selectLabel = computed(() => props.kind === 'sms' ? '填入短信' : '填入拨号')
const overlayDialog = {
  appendTo: typeof document === 'undefined' ? undefined : document.body,
  customClass: 'phone-contact-message-box',
  modalClass: 'phone-contact-message-overlay'
}

const draftNumbers = computed(() => validPhoneContactNumbers(draftNumber.value, extraNumbers.value))
const draftNumberError = computed(() => phoneContactNumberError([draftNumber.value, ...extraNumbers.value]))

const canSave = computed(() => {
  return !!draftName.value.trim() && draftNumbers.value.length > 0 && !draftNumberError.value && !saving.value
})

const {
  importing,
  dropActive,
  resetDropState,
  onDragEnter,
  onDragOver,
  onDragLeave,
  onDrop,
  onImportFile
} = useContactImport({
  deviceId: () => props.deviceId,
  reloadContacts: identities.reloadContacts
})

const peopleCount = computed(() => {
  const groups = new Set<string>()
  for (const item of identities.contacts) {
    groups.add(contactGroupKey(item))
  }
  return groups.size
})

const groupedContacts = computed(() => {
  const needle = query.value.trim().toLowerCase()
  const rows = needle
    ? identities.contacts.filter((item) => contactHaystack(item).includes(needle))
    : identities.contacts
  const groups: ContactGroup[] = []
  const index = new Map<string, ContactGroup>()
  for (const item of rows) {
    const name = String(item.name || item.title || item.number).trim()
    const key = contactGroupKey(item)
    let group = index.get(key)
    if (!group) {
      group = { key, contactId: item.contact_id, name, items: [] }
      index.set(key, group)
      groups.push(group)
    }
    group.items.push(item)
  }
  return groups
})

const selectedCount = computed(() => selected.value.length)
let searchLoadTimer: number | undefined

watch(() => props.modelValue, (open) => {
  if (!open) {
    window.clearTimeout(searchLoadTimer)
    closeAddForm()
    selecting.value = false
    selectingAll.value = false
    expanded.value = ''
    selected.value = []
    resetDropState()
    return
  }
  query.value = ''
  void identities.ensureContacts().catch(() => {})
  void nextTick(() => {
    if (window.matchMedia('(pointer: fine)').matches) searchInput.value?.focus?.()
  })
})

watch(query, (value) => {
  window.clearTimeout(searchLoadTimer)
  if (!value.trim()) return
  searchLoadTimer = window.setTimeout(() => {
    void identities.loadAllContacts().catch(() => {})
  }, CONTACT_SEARCH_DELAY_MS)
})

onBeforeUnmount(() => window.clearTimeout(searchLoadTimer))

function contactHaystack(item: PhoneIdentity) {
  return [
    item.title,
    item.name,
    item.number,
    item.display_number,
    item.subtitle,
    item.carrier,
    item.region,
    item.country
  ].filter(Boolean).join(' ').toLowerCase()
}

function contactGroupKey(item: PhoneIdentity) {
  return item.contact_id || `number:${item.number}`
}

function contactGroupID(group: ContactGroup) {
  const contactId = String(group.contactId || '').trim()
  if (!contactId) throw new Error('联系人缺少稳定 ID，无法修改整个联系人')
  return contactId
}

function handleOpenChange(open: boolean) {
  emit('update:modelValue', open)
}

function choose(item: PhoneIdentity) {
  emit('select', item.number)
  window.setTimeout(() => emit('update:modelValue', false), 80)
}

function activateGroup(group: ContactGroup) {
  if (selecting.value) {
    if (group.items.length > 1) {
      expanded.value = expanded.value === group.key ? '' : group.key
      return
    }
    toggleGroup(group)
    return
  }
  if (group.items.length === 1) {
    choose(group.items[0])
    return
  }
  expanded.value = expanded.value === group.key ? '' : group.key
}

function groupSelected(group: ContactGroup) {
  return group.items.every((item) => selected.value.includes(item.number))
}

function toggleGroup(group: ContactGroup) {
  const numbers = group.items.map((item) => item.number)
  if (groupSelected(group)) {
    selected.value = selected.value.filter((number) => !numbers.includes(number))
    return
  }
  selected.value = Array.from(new Set([...selected.value, ...numbers]))
}

function toggleNumber(number: string) {
  if (selected.value.includes(number)) {
    selected.value = selected.value.filter((item) => item !== number)
    return
  }
  selected.value = [...selected.value, number]
}

async function toggleSelectAll() {
  if (selectingAll.value) return
  selectingAll.value = true
  try {
    await identities.loadAllContacts()
  } catch {
    return
  } finally {
    selectingAll.value = false
  }
  if (!props.modelValue || !selecting.value) return
  const numbers = groupedContacts.value.flatMap((group) => group.items.map((item) => item.number))
  if (numbers.length && numbers.every((number) => selected.value.includes(number))) {
    selected.value = []
    return
  }
  selected.value = numbers
}

function cancelSelection() {
  selecting.value = false
  selected.value = []
}

function loadMoreContacts() {
  void identities.loadMoreContacts().catch(() => {})
}

function onContactScroll(event: Event) {
  const target = event.currentTarget as HTMLElement
  const remaining = target.scrollHeight - target.scrollTop - target.clientHeight
  if (remaining <= CONTACT_SCROLL_THRESHOLD) loadMoreContacts()
}

function toggleAdd() {
  if (adding.value) {
    closeAddForm()
    return
  }
  adding.value = true
  void nextTick(() => nameInput.value?.focus?.())
}

function resetDraft() {
  draftName.value = ''
  draftNumber.value = ''
  extraNumbers.value = []
}

function closeAddForm() {
  adding.value = false
  resetDraft()
}

function addExtraNumberField() {
  extraNumbers.value = [...extraNumbers.value, '']
}

async function saveManual() {
  if (!canSave.value) return
  saving.value = true
  const name = draftName.value.trim()
  const numbers = draftNumbers.value
  try {
    const saved = await phoneContactsService.saveMany(numbers.map((number) => ({
      number, name, groupKey: MANUAL_CONTACT_GROUP_KEY
    })), props.deviceId)
    saved.forEach((ident, index) => identities.upsertLocal(ident, numbers[index], props.deviceId))
    closeAddForm()
    ElMessage.success({
      message: numbers.length > 1 ? `已保存联系人，共 ${numbers.length} 个号码` : '已保存联系人',
      zIndex: 5100
    })
  } catch (error) {
    ElMessage.error({ message: error instanceof Error ? error.message : '保存联系人失败', zIndex: 5100 })
  } finally {
    saving.value = false
  }
}

async function addNumberToGroup(group: ContactGroup) {
  try {
    const contactId = contactGroupID(group)
    const { value } = await ElMessageBox.prompt(`给「${group.name}」再加一个号码`, '添加号码', {
      ...overlayDialog,
      confirmButtonText: '添加',
      cancelButtonText: '取消',
      inputPlaceholder: '+86138… 或 10086',
      inputValidator: (v) => isPhoneContactNumberValid(v) || '号码只能是数字，可带开头的 +'
    })
    const number = normalizePhoneContactNumber(value)
    const ident = await phoneContactsService.save({
      number, name: group.name, deviceId: props.deviceId, contactId
    })
    identities.upsertLocal(ident, number, props.deviceId)
    expanded.value = group.key
    ElMessage.success({ message: '已添加号码', zIndex: 5100 })
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error({ message: error instanceof Error ? error.message : '保存联系人失败', zIndex: 5100 })
  }
}

async function editGroup(group: ContactGroup) {
  try {
    const contactId = contactGroupID(group)
    const { value } = await ElMessageBox.prompt('保存后，来电和短信通知会显示这个名字', '改联系人', {
      ...overlayDialog,
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputPlaceholder: '联系人名字',
      inputValue: group.name,
      inputValidator: (v) => !!String(v || '').trim() || '请填写名字'
    })
    const name = String(value).trim()
    const saved = await phoneContactsService.updateGroup(contactId, name)
    identities.applyContactGroupUpdate(saved)
    ElMessage.success({ message: '已更新联系人', zIndex: 5100 })
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error({ message: error instanceof Error ? error.message : '保存联系人失败', zIndex: 5100 })
  }
}

async function deleteGroup(group: ContactGroup) {
  try {
    const contactId = contactGroupID(group)
    await ElMessageBox.confirm(
      `确定删除「${group.name}」及其所有号码？删除后，来电和短信会重新只显示号码。`,
      '删除联系人',
      {
        ...overlayDialog,
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
        type: 'warning',
        distinguishCancelAndClose: true
      }
    )
    await identities.removeContactGroup(contactId)
    ElMessage.success({ message: '已删除联系人', zIndex: 5100 })
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error({ message: error instanceof Error ? error.message : '删除联系人失败', zIndex: 5100 })
  }
}

async function deleteSelected() {
  if (!selected.value.length) return
  try {
    await ElMessageBox.confirm(`确定删除选中的 ${selected.value.length} 个号码？`, '批量删除', {
      ...overlayDialog,
      confirmButtonText: '确认删除',
      cancelButtonText: '取消',
      type: 'warning',
      distinguishCancelAndClose: true
    })
    await identities.removeContacts(selected.value, props.deviceId)
    selected.value = []
    selecting.value = false
    ElMessage.success({ message: '已删除选中联系人', zIndex: 5100 })
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error({ message: error instanceof Error ? error.message : '删除联系人失败', zIndex: 5100 })
  }
}

async function exportContacts() {
  if (exporting.value) return
  exporting.value = true
  try {
    await phoneContactsService.exportFile('vcf')
  } catch (error) {
    ElMessage.error({ message: error instanceof Error ? error.message : '导出失败', zIndex: 5100 })
  } finally {
    exporting.value = false
  }
}

async function reload() {
  try {
    await identities.reloadContacts()
  } catch {
    // contactsError is rendered in the list.
  }
}
</script>

<template>
  <el-drawer
    :model-value="modelValue"
    class="phone-contacts-drawer"
    modal-class="phone-contacts-drawer-scrim"
    direction="rtl"
    size="min(420px, 100vw)"
    append-to-body
    :z-index="4200"
    :show-close="false"
    @update:model-value="handleOpenChange"
  >
    <template #header>
      <div class="picker-header">
        <div class="picker-icon" aria-hidden="true"><el-icon><Person24Regular /></el-icon></div>
        <div>
          <span>CONTACTS</span>
          <h2>全部联系人 · {{ peopleCount }}{{ identities.contactsHasMore ? '+' : '' }}</h2>
        </div>
        <el-button circle aria-label="关闭联系人" @click="handleOpenChange(false)">
          <el-icon><Dismiss24Regular /></el-icon>
        </el-button>
      </div>
    </template>

    <div
      class="picker-shell"
      @dragenter="onDragEnter"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
    <div class="picker-toolbar">
      <el-input
        ref="searchInput"
        v-model="query"
        clearable
        placeholder="搜索名字、号码、归属地"
        aria-label="搜索联系人"
      >
        <template #prefix><el-icon><Search24Regular /></el-icon></template>
      </el-input>
      <button type="button" class="picker-add-toggle" :aria-pressed="adding" @click="toggleAdd">
        <el-icon><Add24Regular /></el-icon>
        {{ adding ? '取消' : '添加' }}
      </button>
    </div>
    <div class="picker-actions" role="toolbar" aria-label="联系人管理">
      <input ref="fileInput" type="file" accept=".vcf,.vcard,.csv,.txt,text/vcard,text/x-vcard,text/csv" hidden multiple @change="onImportFile">
      <template v-if="selecting">
        <button type="button" class="picker-action" :disabled="selectingAll" @click="toggleSelectAll">
          <el-icon><SelectAllOn24Regular /></el-icon>
          {{ selectingAll ? '加载中…' : '全选' }}
        </button>
        <button type="button" class="picker-action is-danger" :disabled="!selectedCount" @click="deleteSelected">
          <el-icon><Delete24Regular /></el-icon>
          {{ selectedCount ? `删除 ${selectedCount}` : '删除' }}
        </button>
        <button type="button" class="picker-action" @click="cancelSelection">
          <el-icon><Dismiss24Regular /></el-icon>
          取消
        </button>
      </template>
      <template v-else>
        <button type="button" class="picker-action" :disabled="importing" aria-label="从 vcf 或 csv 文件导入联系人" @click="fileInput?.click()">
          <el-icon><ArrowUpload24Regular /></el-icon>
          {{ importing ? '导入中…' : '导入' }}
        </button>
        <button type="button" class="picker-action" :disabled="exporting" @click="exportContacts">
          <el-icon><ArrowDownload24Regular /></el-icon>
          {{ exporting ? '导出中…' : '导出' }}
        </button>
        <button type="button" class="picker-action" :aria-pressed="selecting" @click="selecting = true">
          <el-icon><CheckboxChecked24Regular /></el-icon>
          选择
        </button>
      </template>
    </div>
    <p class="picker-import-hint">
      <el-icon><ArrowUpload24Regular /></el-icon>
      支持将 vcf / csv 拖到此处导入
    </p>

    <form v-if="adding" class="picker-form" @submit.prevent="saveManual">
      <label>
        名字
        <el-input ref="nameInput" v-model="draftName" name="drawer-contact-name" autocomplete="name" maxlength="64" placeholder="张三" />
      </label>
      <label>
        号码
        <el-input
          v-model="draftNumber"
          type="tel"
          inputmode="tel"
          name="drawer-contact-number"
          autocomplete="tel"
          maxlength="33"
          placeholder="+86138… 或 10086"
          :aria-invalid="!!draftNumber.trim() && !isPhoneContactNumberValid(draftNumber)"
          :aria-describedby="draftNumberError ? 'drawer-contact-number-error' : undefined"
        />
      </label>
      <label v-for="(_, index) in extraNumbers" :key="index">
        号码 {{ index + 2 }}
        <el-input
          v-model="extraNumbers[index]"
          type="tel"
          inputmode="tel"
          maxlength="33"
          placeholder="再填一个号码"
          :aria-invalid="!!extraNumbers[index].trim() && !isPhoneContactNumberValid(extraNumbers[index])"
          :aria-describedby="draftNumberError ? 'drawer-contact-number-error' : undefined"
        />
      </label>
      <button type="button" class="picker-add-more" @click="addExtraNumberField">再加一个号码</button>
      <button type="submit" :disabled="!canSave">{{ saving ? '保存中…' : '保存' }}</button>
      <small v-if="draftNumberError" id="drawer-contact-number-error" role="alert">{{ draftNumberError }}</small>
    </form>

    <div v-if="identities.contactsError" class="picker-error" role="alert">
      <span>{{ identities.contactsError }}</span>
      <button type="button" class="icon-button" aria-label="重新加载联系人" @click="reload">
        <el-icon><ArrowClockwise24Regular /></el-icon>
      </button>
    </div>
    <div v-if="identities.contactsLoading && !identities.contacts.length" class="picker-state">正在加载联系人</div>
    <div v-else-if="groupedContacts.length" class="picker-list" role="list" @scroll.passive="onContactScroll">
      <article v-for="group in groupedContacts" :key="group.key" class="picker-item" :class="{ 'is-open': expanded === group.key }" role="listitem">
        <div v-if="selecting" class="picker-check" @click.stop>
          <el-checkbox
            :model-value="groupSelected(group)"
            :aria-label="`选择联系人 ${group.name}`"
            @change="toggleGroup(group)"
          />
        </div>
        <button type="button" class="picker-main" :aria-label="`${selectLabel} ${group.name}`" @click="activateGroup(group)">
          <strong>{{ group.name }}</strong>
          <span v-if="group.items.length === 1">{{ group.items[0].display_number || group.items[0].number }}</span>
          <span v-else>{{ group.items.length }} 个号码 · {{ group.items.map((item) => item.display_number || item.number).join(' / ') }}</span>
          <small v-if="group.items.length === 1 && group.items[0].subtitle">{{ group.items[0].subtitle }}</small>
        </button>
        <template v-if="!selecting">
          <button type="button" class="icon-button" :aria-label="`给 ${group.name} 添加号码`" @click.stop="addNumberToGroup(group)">
            <el-icon><Add24Regular /></el-icon>
          </button>
          <button type="button" class="icon-button" :aria-label="`修改 ${group.name}`" @click.stop="editGroup(group)">
            <el-icon><PersonEdit24Regular /></el-icon>
          </button>
          <button type="button" class="icon-button is-danger" :aria-label="`删除 ${group.name}`" @click.stop="deleteGroup(group)">
            <el-icon><Delete24Regular /></el-icon>
          </button>
        </template>
        <div v-if="expanded === group.key && group.items.length > 1" class="picker-numbers">
          <template v-if="selecting">
            <div v-for="item in group.items" :key="item.number" class="picker-number is-check">
              <el-checkbox
                :model-value="selected.includes(item.number)"
                :aria-label="`选择 ${item.display_number || item.number}`"
                @change="toggleNumber(item.number)"
              />
              <span>
                {{ item.display_number || item.number }}
                <small v-if="item.subtitle">{{ item.subtitle }}</small>
              </span>
            </div>
          </template>
          <template v-else>
            <button
              v-for="item in group.items"
              :key="item.number"
              type="button"
              class="picker-number"
              @click.stop="choose(item)"
            >
              <span>{{ item.display_number || item.number }}</span>
              <small v-if="item.subtitle">{{ item.subtitle }}</small>
            </button>
            <button type="button" class="picker-number is-add" @click.stop="addNumberToGroup(group)">再加一个号码</button>
          </template>
        </div>
      </article>
      <div v-if="identities.contactsLoadingMore" class="picker-list-footer" role="listitem">
        <div class="picker-list-status" role="status">正在加载更多联系人</div>
      </div>
      <div v-else-if="identities.contactsHasMore" class="picker-list-footer" role="listitem">
        <button type="button" class="picker-load-more" @click="loadMoreContacts">继续向下滚动加载更多</button>
      </div>
    </div>
    <div v-else-if="identities.contacts.length" class="picker-state">没有匹配「{{ query.trim() }}」的联系人</div>
    <div v-else-if="!identities.contactsError" class="picker-state">还没有联系人。可以添加，或把手机导出的 vcf / csv 拖进来。同一个人有多个号码会合并成一条。</div>
    <div v-show="dropActive" class="picker-drop-overlay">放开即可导入通讯录</div>
    </div>
  </el-drawer>
</template>

<style scoped>
:global(.phone-contacts-drawer-scrim) {
  background: color-mix(in srgb, #000 54%, transparent);
  transition: opacity 200ms var(--ui-ease-out) !important;
}
:global(.phone-contacts-drawer) {
  border-radius: var(--ui-radius-lg) 0 0 var(--ui-radius-lg);
  background: var(--ui-surface);
  transition: transform 240ms var(--ui-ease-drawer) !important;
}
:global(.phone-contacts-drawer .el-drawer__header) { min-height: 70px; margin: 0; padding: 12px 18px; border-bottom: 1px solid var(--ui-border); color: var(--ui-text); }
:global(.phone-contacts-drawer .el-drawer__body) { min-height: 0; padding: 0; display: flex; flex-direction: column; overflow: hidden; }
:global(.phone-contact-message-overlay.el-overlay) { z-index: 5200 !important; }
:global(.phone-contact-message-overlay .el-overlay-message-box) { padding: 16px; }
:global(.phone-contact-message-box) { max-width: min(420px, calc(100vw - 32px)); border-radius: var(--ui-radius-md); }
:global(.phone-contact-message-box .el-message-box__header) { padding: 4px 36px 10px 4px; }
:global(.phone-contact-message-box .el-message-box__content) { padding: 0 4px; }
:global(.phone-contact-message-box .el-message-box__input .el-input__wrapper) { min-height: 44px; }
:global(.phone-contact-message-box .el-message-box__btns) { gap: 8px; padding: 14px 4px 4px; }
:global(.phone-contact-message-box .el-message-box__btns .el-button) { min-width: 76px; min-height: 44px; }
.picker-shell { position: relative; flex: 1; min-height: 0; display: flex; flex-direction: column; }
.picker-drop-overlay {
  position: absolute;
  inset: 0;
  z-index: 3;
  display: grid;
  place-items: center;
  border: 2px dashed var(--ui-primary);
  border-radius: var(--ui-radius-md);
  background: color-mix(in srgb, var(--ui-surface) 88%, transparent);
  color: var(--ui-primary);
  font-size: 16px;
  font-weight: 650;
  pointer-events: none;
}
.picker-header { width: 100%; min-width: 0; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 10px; }
.picker-icon { width: 38px; height: 38px; display: grid; place-items: center; border: 1px solid color-mix(in srgb, var(--ui-primary) 42%, var(--ui-border)); border-radius: var(--ui-radius-md); background: color-mix(in srgb, var(--ui-primary) 9%, transparent); color: var(--ui-primary); }
.picker-header div > span { color: var(--ui-primary); font: var(--ui-font-caption)/1.2 "v-mono", monospace; letter-spacing: 0; }
.picker-header h2 { margin: 4px 0 0; color: var(--ui-text); font-size: 18px; }
.picker-header :deep(.el-button) { width: 44px; height: 44px; }
.picker-toolbar { padding: 14px 18px 8px; display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; align-items: center; }
.picker-actions {
  padding: 0 18px 12px;
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}
.picker-action {
  min-height: 44px;
  min-width: 0;
  padding: 0 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--ui-text-muted);
  cursor: pointer;
  font-size: 13px;
  white-space: nowrap;
  transition: background-color 140ms ease, color 140ms ease;
}
.picker-action .el-icon { flex: 0 0 auto; font-size: 17px; }
.picker-action:active,
.picker-action[aria-pressed="true"] { background: var(--ui-surface-muted); color: var(--ui-text); }
.picker-action:disabled { opacity: .45; cursor: not-allowed; }
.picker-action.is-danger { color: var(--ui-danger); }
.picker-import-hint {
  min-height: 30px;
  margin: 0;
  padding: 0 18px 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border-bottom: 1px solid var(--ui-border-muted);
  color: var(--ui-text-muted);
  font-size: 12px;
}
.picker-import-hint .el-icon { flex: 0 0 auto; font-size: 15px; }
.picker-add-toggle {
  min-height: 44px;
  padding: 0 12px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--ui-border);
  border-radius: var(--ui-radius-pill);
  background: var(--ui-surface);
  color: var(--ui-text);
  cursor: pointer;
  font-size: 13px;
  white-space: nowrap;
}
.picker-add-toggle[aria-pressed="true"] {
  border-color: color-mix(in srgb, var(--ui-primary) 40%, var(--ui-border));
  color: var(--ui-primary);
}
.picker-form { padding: 0 18px 14px; display: grid; grid-template-columns: 1fr; gap: 10px; border-bottom: 1px solid var(--ui-border); flex: 0 0 auto; }
.picker-form label { min-width: 0; display: grid; gap: 6px; color: var(--ui-text-muted); font-size: 12px; font-weight: 600; }
.picker-form :deep(.el-input) { width: 100%; min-width: 0; }
.picker-form :deep(.el-input__wrapper) { min-height: 44px; border-radius: var(--ui-radius-md); background: var(--ui-surface); }
.picker-form button[type="submit"] { width: 100%; min-height: 44px; border: 1px solid var(--ui-primary); border-radius: var(--ui-radius-pill); background: var(--ui-primary-solid); color: #fff; cursor: pointer; font-size: 14px; font-weight: 650; }
.picker-add-more { width: 100%; min-height: 44px; border: 1px dashed var(--ui-border); border-radius: var(--ui-radius-pill); background: transparent; color: var(--ui-text-muted); cursor: pointer; font-size: 13px; }
.picker-form button:disabled { cursor: not-allowed; opacity: .45; }
.picker-number.is-check { grid-template-columns: auto minmax(0, 1fr); align-items: center; }
.picker-number.is-check :deep(.el-checkbox) { min-width: 32px; min-height: 32px; margin-right: 0; }
.picker-number.is-add { color: var(--ui-primary); justify-items: start; }
.picker-form small { color: var(--ui-danger); font-size: 12px; }
.picker-list { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.picker-list-status,
.picker-load-more {
  width: 100%;
  min-height: 44px;
  display: grid;
  place-items: center;
  border: 0;
  background: transparent;
  color: var(--ui-text-muted);
  font-size: 12px;
}
.picker-load-more { cursor: pointer; }
.picker-item { min-height: 64px; padding: 8px 8px 8px 18px; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; border-bottom: 1px solid var(--ui-border-muted); transition: background-color 120ms ease; }
.picker-check { width: 44px; height: 44px; display: grid; place-items: center; }
.picker-check :deep(.el-checkbox) { margin-right: 0; }
.picker-numbers { flex: 1 1 100%; display: grid; gap: 6px; padding: 4px 40px 8px 0; }
.picker-number { min-height: 44px; padding: 8px 12px; display: grid; gap: 2px; border: 1px solid var(--ui-border); border-radius: var(--ui-radius-md); background: var(--ui-surface); color: inherit; text-align: left; cursor: pointer; }
.picker-number span { font-family: "v-mono", monospace; font-size: 13px; color: var(--ui-text); }
.picker-number small { color: var(--ui-primary); font-size: 12px; }
.picker-item:active { background: var(--ui-surface-muted); }
.picker-main { min-width: 0; flex: 1; min-height: 44px; padding: 6px 8px 6px 0; border: 0; background: transparent; color: inherit; text-align: left; cursor: pointer; }
.picker-main strong { display: block; overflow: hidden; color: var(--ui-text); font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.picker-main span { display: block; margin-top: 2px; overflow: hidden; color: var(--ui-text-muted); font-family: "v-mono", monospace; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.picker-main small { display: block; margin-top: 2px; overflow: hidden; color: var(--ui-primary); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.icon-button { width: 44px; height: 44px; flex: 0 0 44px; display: grid; place-items: center; border: 0; background: transparent; color: var(--ui-text-muted); cursor: pointer; transition: color 120ms ease, transform 140ms var(--ui-ease-out); }
.icon-button:active { transform: scale(0.94); }
.icon-button.is-danger:active { color: var(--ui-danger); }
@media (hover: hover) and (pointer: fine) {
  .icon-button:hover { color: var(--ui-text); }
  .icon-button.is-danger:hover { color: var(--ui-danger); }
  .picker-action:not(:disabled):hover { background: var(--ui-surface-muted); color: var(--ui-text); }
  .picker-action.is-danger:not(:disabled):hover { color: var(--ui-danger); }
  .picker-load-more:hover { color: var(--ui-primary); }
  .picker-item:hover { background: color-mix(in srgb, var(--ui-primary) 6%, var(--ui-surface)); }
}
.picker-state { min-height: 120px; padding: 24px 18px; display: flex; align-items: center; justify-content: center; gap: 12px; color: var(--ui-text-muted); font-size: 13px; text-align: center; }
.picker-error { min-height: 52px; margin: 0 18px 8px; padding: 4px 0 4px 12px; display: flex; align-items: center; justify-content: space-between; gap: 12px; border: 1px solid color-mix(in srgb, var(--ui-danger) 32%, var(--ui-border)); border-radius: var(--ui-radius-md); background: color-mix(in srgb, var(--ui-danger) 7%, transparent); color: var(--ui-danger); font-size: 13px; }
.picker-error span { min-width: 0; overflow-wrap: anywhere; }
@media (max-width: 640px) {
  :global(.phone-contacts-drawer) { border-radius: 0; }
  :global(.phone-contacts-drawer .el-drawer__body) { padding-bottom: env(safe-area-inset-bottom); }
}
@media (pointer: coarse) {
  .picker-actions { border-bottom: 1px solid var(--ui-border-muted); }
  .picker-import-hint { display: none; }
}
@media (prefers-reduced-motion: reduce) {
  :global(.phone-contacts-drawer),
  :global(.phone-contacts-drawer-scrim) {
    transition-duration: 1ms !important;
  }
}
</style>
