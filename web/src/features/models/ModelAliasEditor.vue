<script setup lang="ts" generic="T extends ModelDraftValue">
import { Plus, X } from '@lucide/vue'
import { computed, nextTick, ref, useId, watch } from 'vue'

import LedgerRecordList from '@/components/collection/LedgerRecordList.vue'
import AppButton from '@/components/ui/AppButton.vue'
import AppSearchInput from '@/components/ui/AppSearchInput.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import CompactFieldError from '@/components/ui/CompactFieldError.vue'
import IconButton from '@/components/ui/IconButton.vue'
import TagInput from '@/components/ui/TagInput.vue'

import {
  hasClaudeAdapter,
  isClaudeAdapterAlias,
  modelDraftValidity,
  normalizeAliases,
  visibleAliases,
  withClaudeAdapter,
  type ModelAliasEditorLabels,
  type ModelDraftKey,
  type ModelDraftValue,
  type ModelNameConflict,
} from './model-draft'

/**
 * ClaudeAdapterLabels 同时承担「启用该列」与「提供其文案」两件事：提供即渲染，不提供
 * 即整列缺席。这样批量导入页复用本组件时既不会多出一个空轨道，也不必提供一份用不到的
 * 必填文案。
 */
export interface ClaudeAdapterLabels {
  title: string
  forModel: (id: string) => string
  conflict: () => string
}

const props = withDefaults(
  defineProps<{
    modelValue: T[]
    conflicts: readonly ModelNameConflict[]
    labels: ModelAliasEditorLabels
    createRow?: () => T
    disabled?: boolean
    searchable?: boolean
    addable?: boolean
    search?: string
    validationMode?: 'immediate' | 'blur'
    showAllErrors?: boolean
    claudeAdapter?: ClaudeAdapterLabels
  }>(),
  {
    createRow: undefined,
    disabled: false,
    searchable: true,
    addable: true,
    search: undefined,
    validationMode: 'immediate',
    showAllErrors: false,
    claudeAdapter: undefined,
  },
)
const emit = defineEmits<{
  'update:modelValue': [value: T[]]
  'update:search': [value: string]
  'visible-validation-change': [indexes: Set<number>]
}>()

const instanceId = useId()
const root = ref<HTMLElement>()
const internalSearch = ref(props.search ?? '')
const touchedModelIDs = ref<Set<ModelDraftKey>>(new Set())
const touchedAliases = ref<Set<ModelDraftKey>>(new Set())
const touchedClaudeAdapter = ref<Set<ModelDraftKey>>(new Set())
const searchValue = computed({
  get: () => internalSearch.value,
  set: (value: string) => {
    internalSearch.value = value
    emit('update:search', value)
  },
})
watch(
  () => props.search,
  (value) => {
    internalSearch.value = value ?? ''
  },
)
const validity = computed(() => modelDraftValidity(props.modelValue, props.conflicts))
/** 只在提供了 claudeAdapter 时插入开关列与它的栅格轨道。 */
const gridClass = computed(() =>
  props.claudeAdapter === undefined
    ? 'model-alias-editor__grid'
    : 'model-alias-editor__grid model-alias-editor__grid--claude',
)
const visibleRows = computed(() => {
  const query = searchValue.value.trim().toLocaleLowerCase()
  return props.modelValue.flatMap((item, index) =>
    !query ||
    `${item.id} ${item.name} ${visibleAliases(item.aliases).join(' ')}`
      .toLocaleLowerCase()
      .includes(query)
      ? [{ item, index }]
      : [],
  )
})

function updateRow(index: number, patch: Partial<Pick<ModelDraftValue, 'id' | 'aliases'>>): void {
  emit(
    'update:modelValue',
    props.modelValue.map((item, current) =>
      current === index
        ? ({ ...item, ...patch } as T)
        : ({ ...item, sources: [...item.sources] } as T),
    ),
  )
}

/**
 * 别名在写入草稿前先规范化：trim、去空、去 ID、去重，与服务端同规则。
 *
 * 入参是输入框发出的「可见」列表，因此必须先把开关状态合并回去：否则用户只是编辑了
 * 一个普通别名，被隐藏的 claude-*[1m] 就会被当成「已删除的别名」丢掉。
 */
function setAliases(index: number, aliases: string[]): void {
  const item = props.modelValue[index]
  if (item === undefined) return
  updateRow(index, {
    aliases: normalizeAliases(withClaudeAdapter(aliases, hasClaudeAdapter(item.aliases)), item.id),
  })
}

/**
 * 开关只改写别名数组，不引入任何新的持久化字段。
 *
 * 开关在分组内互斥：打开一个会自动关掉其余全部，因此同一时刻只会有一个模型启用
 * Claude 适配。做成互斥而不是报错，是因为后端的同名模式冲突规则（同一分组内两个
 * 上游认领 claude-*[1m]）依然成立，但用户不该被迫先手工关掉另一个才能打开这一个。
 * 关闭某个开关只影响它自己。
 */
function setClaudeAdapter(index: number, enabled: boolean): void {
  const item = props.modelValue[index]
  if (item === undefined) return
  emit(
    'update:modelValue',
    props.modelValue.map((current, position) => {
      if (position === index) {
        return {
          ...current,
          aliases: withClaudeAdapter(current.aliases, enabled),
          sources: [...current.sources],
        } as T
      }
      if (enabled && hasClaudeAdapter(current.aliases)) {
        return {
          ...current,
          aliases: withClaudeAdapter(current.aliases, false),
          sources: [...current.sources],
        } as T
      }
      return { ...current, sources: [...current.sources] } as T
    }),
  )
}

function removeRow(index: number): void {
  emit(
    'update:modelValue',
    props.modelValue
      .filter((_, current) => current !== index)
      .map((item) => ({ ...item, sources: [...item.sources] })),
  )
}

async function addManual(): Promise<void> {
  if (props.disabled || !props.createRow) return
  searchValue.value = ''
  const index = props.modelValue.length
  emit('update:modelValue', [
    ...props.modelValue.map((item) => ({ ...item, sources: [...item.sources] })),
    props.createRow(),
  ])
  await nextTick()
  root.value?.querySelector<HTMLInputElement>(`[data-model-id-index="${index}"]`)?.focus()
}

function conflictMessage(index: number): string {
  const conflict = props.conflicts.find((item) => item.indexes.includes(index))
  return conflict ? props.labels.nameConflict(conflict.client_model) : ''
}

function conflictName(index: number): string {
  return props.conflicts.find((item) => item.indexes.includes(index))?.client_model ?? ''
}

function modelIDError(item: ModelDraftValue, index: number): string {
  if (validity.value.emptyIDIndexes.has(index)) return props.labels.manualIdRequired
  // 冲突名等于本行 ID 时，错误归属于 ID 列；否则由别名列呈现。
  return conflictName(index) === item.id.trim() ? conflictMessage(index) : ''
}

function modelAliasError(item: ModelDraftValue, index: number): string {
  const name = conflictName(index)
  // 冲突名是 Claude 适配常量时它并不显示在别名框里，错误改由开关列呈现（AC20）。
  if (name === '' || name === item.id.trim() || isClaudeAdapterAlias(name)) return ''
  return conflictMessage(index)
}

/**
 * claudeAdapterError 把「本分组内已有别的模型占用 Claude 适配别名」这一冲突挂到开关列。
 *
 * 开关自身已经互斥，所以这条错误只会出现在开关管不到的地方：用户在别名框里手输了
 * 两次 claude-*[1m]，或库里存在本次改动之前保存的重复数据。文案不点名占用者——冲突的
 * 另一个模型必然开着同一个开关，看这一列就能找到它；而反查占用者 ID 在空 ID 行上会
 * 退化成一句残缺的话。
 */
function claudeAdapterError(index: number): string {
  const adapter = props.claudeAdapter
  if (adapter === undefined || !isClaudeAdapterAlias(conflictName(index))) return ''
  return adapter.conflict()
}

function visibleModelIDError(item: ModelDraftValue, index: number): string {
  const error = modelIDError(item, index)
  if (!error) return ''
  if (
    props.validationMode === 'immediate' ||
    props.showAllErrors ||
    !item.editable_id ||
    touchedModelIDs.value.has(item.key)
  ) {
    return error
  }
  return ''
}

function visibleModelAliasError(item: ModelDraftValue, index: number): string {
  const error = modelAliasError(item, index)
  if (!error) return ''
  if (
    props.validationMode === 'immediate' ||
    props.showAllErrors ||
    touchedAliases.value.has(item.key)
  ) {
    return error
  }
  return ''
}

function visibleClaudeAdapterError(item: ModelDraftValue, index: number): string {
  const error = claudeAdapterError(index)
  if (!error) return ''
  if (
    props.validationMode === 'immediate' ||
    props.showAllErrors ||
    touchedClaudeAdapter.value.has(item.key)
  ) {
    return error
  }
  return ''
}

const visibleInvalidIndexes = computed(
  () =>
    new Set(
      props.modelValue.flatMap((item, index) =>
        visibleModelIDError(item, index) ||
        visibleModelAliasError(item, index) ||
        visibleClaudeAdapterError(item, index)
          ? [index]
          : [],
      ),
    ),
)

watch(visibleInvalidIndexes, (indexes) => emit('visible-validation-change', new Set(indexes)), {
  immediate: true,
})

function touchModelID(key: ModelDraftKey): void {
  if (touchedModelIDs.value.has(key)) return
  touchedModelIDs.value = new Set(touchedModelIDs.value).add(key)
}

function touchAlias(key: ModelDraftKey): void {
  if (touchedAliases.value.has(key)) return
  touchedAliases.value = new Set(touchedAliases.value).add(key)
}

function touchClaudeAdapter(key: ModelDraftKey): void {
  if (touchedClaudeAdapter.value.has(key)) return
  touchedClaudeAdapter.value = new Set(touchedClaudeAdapter.value).add(key)
}

async function focusFirstInvalid(): Promise<void> {
  const index = Math.min(...validity.value.invalidIndexes)
  if (!Number.isFinite(index)) return
  searchValue.value = ''
  const item = props.modelValue[index]
  const conflict = conflictName(index)
  const targetsModelID =
    validity.value.emptyIDIndexes.has(index) ||
    (conflict !== '' && item !== undefined && conflict === item.id.trim())
  const targetsClaudeAdapter =
    !targetsModelID && props.claudeAdapter !== undefined && isClaudeAdapterAlias(conflict)
  if (item) {
    if (targetsModelID) touchModelID(item.key)
    else if (targetsClaudeAdapter) touchClaudeAdapter(item.key)
    else touchAlias(item.key)
  }
  await nextTick()
  const selector = targetsModelID
    ? `[data-model-id-index="${index}"]`
    : targetsClaudeAdapter
      ? `[data-claude-adapter-index="${index}"]`
      : `[data-alias-input-index="${index}"]`
  root.value?.querySelector<HTMLElement>(selector)?.focus()
}

defineExpose({ addManual, focusFirstInvalid })
</script>

<template>
  <div ref="root" class="model-alias-editor">
    <div v-if="searchable" class="model-alias-editor__toolbar">
      <label class="model-alias-editor__search">
        <span>{{ labels.searchLabel }}</span>
        <AppSearchInput
          v-model="searchValue"
          :label="labels.search"
          :placeholder="labels.search"
          :clear-label="labels.clearSearch"
          :disabled="disabled"
        />
      </label>
      <span class="model-alias-editor__count" aria-live="polite">
        {{ labels.count(modelValue.length) }}
      </span>
    </div>

    <LedgerRecordList
      :label="labels.tableLabel"
      :row-count="(visibleRows.length || 1) + 1"
      :grid-class="gridClass"
    >
      <template #header>
        <span role="columnheader">{{ labels.id }}</span>
        <span role="columnheader">{{ labels.alias }}</span>
        <span v-if="claudeAdapter" role="columnheader">{{ claudeAdapter.title }}</span>
        <span role="columnheader">{{ labels.thirdColumn }}</span>
        <span role="columnheader"
          ><span class="sr-only">{{ labels.actions }}</span></span
        >
      </template>

      <article
        v-for="({ item, index }, visibleIndex) in visibleRows"
        :key="item.key"
        class="ledger-record-list__record model-alias-editor__record"
        :class="{ 'model-alias-editor__record--invalid': visibleInvalidIndexes.has(index) }"
        role="row"
        :aria-rowindex="visibleIndex + 2"
      >
        <div class="ledger-record-list__cell model-alias-editor__id" role="cell">
          <span class="model-alias-editor__mobile-label">{{ labels.id }}</span>
          <CompactFieldError
            :id="`${instanceId}-model-id-${index}`"
            class="model-alias-editor__id-field"
            :error="visibleModelIDError(item, index)"
          >
            <template #default="{ invalid, describedBy }">
              <AppTextInput
                v-if="item.editable_id"
                :id="`${instanceId}-model-id-${index}`"
                :model-value="item.id"
                appearance="surface"
                size="compact"
                monospace
                :label="labels.id"
                :placeholder="labels.manualId"
                :invalid="invalid"
                :described-by="describedBy"
                :data-model-id-index="index"
                :spellcheck="false"
                :disabled="disabled"
                @update:model-value="updateRow(index, { id: $event })"
                @blur="touchModelID(item.key)"
              />
              <code v-else :aria-describedby="describedBy">{{ item.id }}</code>
            </template>
          </CompactFieldError>
        </div>

        <div class="ledger-record-list__cell model-alias-editor__alias-cell" role="cell">
          <span class="model-alias-editor__mobile-label">{{ labels.alias }}</span>
          <CompactFieldError
            :id="`${instanceId}-model-alias-${index}`"
            class="model-alias-editor__alias-field"
            :error="visibleModelAliasError(item, index)"
          >
            <template #default="{ invalid, describedBy }">
              <TagInput
                :id="`${instanceId}-model-alias-${index}`"
                :model-value="visibleAliases(item.aliases)"
                :label="labels.aliasFor(item.id)"
                :placeholder="labels.aliasPlaceholder"
                :remove-label="labels.removeAliasFor"
                :disabled="disabled"
                :invalid="invalid"
                :described-by="describedBy"
                :data-alias-input-index="index"
                @update:model-value="setAliases(index, $event)"
                @blur="touchAlias(item.key)"
              />
            </template>
          </CompactFieldError>
        </div>

        <div
          v-if="claudeAdapter"
          class="ledger-record-list__cell model-alias-editor__claude-cell"
          role="cell"
        >
          <span class="model-alias-editor__mobile-label">{{ claudeAdapter.title }}</span>
          <CompactFieldError
            :id="`${instanceId}-claude-adapter-${index}`"
            class="model-alias-editor__claude-field"
            :error="visibleClaudeAdapterError(item, index)"
          >
            <template #default="{ describedBy }">
              <AppSwitch
                :model-value="hasClaudeAdapter(item.aliases)"
                :label="claudeAdapter.forModel(item.id)"
                :disabled="disabled"
                :aria-describedby="describedBy"
                :data-claude-adapter-index="index"
                @update:model-value="setClaudeAdapter(index, $event)"
                @blur="touchClaudeAdapter(item.key)"
              />
            </template>
          </CompactFieldError>
        </div>

        <div class="ledger-record-list__cell model-alias-editor__third-column" role="cell">
          <span class="model-alias-editor__mobile-label">{{ labels.thirdColumn }}</span>
          <slot name="third-column" :item="item" :index="index" />
        </div>

        <div class="ledger-record-list__cell model-alias-editor__actions" role="cell">
          <IconButton
            variant="ghost"
            size="compact"
            :disabled="disabled"
            :label="labels.removeFor(item.id || labels.manualId)"
            @click="removeRow(index)"
          >
            <X :size="16" aria-hidden="true" />
          </IconButton>
        </div>
      </article>

      <div
        v-if="visibleRows.length === 0"
        class="ledger-record-list__record model-alias-editor__empty"
        role="row"
        aria-rowindex="2"
      >
        <span class="ledger-record-list__cell" role="cell">
          {{ modelValue.length ? labels.noMatches : labels.empty }}
        </span>
      </div>
    </LedgerRecordList>

    <AppButton
      v-if="addable"
      class="model-alias-editor__add"
      variant="link"
      size="inline"
      :disabled="disabled || !createRow"
      @click="addManual"
    >
      <Plus :size="16" aria-hidden="true" />{{ labels.addInline }}
    </AppButton>
  </div>
</template>

<style scoped>
.model-alias-editor {
  min-width: 0;
}

.model-alias-editor__toolbar {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: var(--space-3);
  padding: 15px 0 13px;
}

.model-alias-editor__search {
  display: grid;
  width: min(100%, 420px);
  min-width: 0;
  gap: 5px;
  color: var(--color-text-faint);
  font-size: var(--text-meta);
}

.model-alias-editor__count {
  flex: none;
  color: var(--color-text-faint);
  font-family: var(--font-mono);
  font-size: 10.8px;
}

.model-alias-editor__grid {
  /* 一个模型可配多个别名，标签会换行，因此行高比单输入框时代更高。 */
  --ledger-record-list-record-min-height: 64px;
  --ledger-record-list-record-padding: 9px 0;
  --ledger-record-list-grid: minmax(180px, 24fr) minmax(280px, 52fr) minmax(120px, 18fr) 40px;
  --ledger-record-list-column-gap: 16px;
}

/*
 * Claude 适配列插在别名与价格状态之间，因此第 2 个轨道多一个开关宽度。列顺序完全由
 * DOM 顺序决定（表头与记录行都是 subgrid），插入表头与插入单元格必须成对，否则后面
 * 所有列会静默错位。批量导入页不传 claudeAdapter，走的仍是上面那条 4 轨道规则。
 */
.model-alias-editor__grid--claude {
  --ledger-record-list-grid: minmax(180px, 24fr) minmax(280px, 52fr) 116px minmax(120px, 18fr) 40px;
}

.model-alias-editor__grid :deep(.ledger-record-list__header) {
  font-size: var(--text-label-xs);
  font-weight: 500;
}

.model-alias-editor__record--invalid {
  background: var(--color-danger-bg);
}

.model-alias-editor__id,
.model-alias-editor__claude-cell,
.model-alias-editor__third-column {
  min-width: 0;
}

.model-alias-editor__claude-cell {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
}

.model-alias-editor__claude-field {
  width: auto;
}

.model-alias-editor__id {
  display: grid;
  align-content: center;
  gap: 5px;
}

.model-alias-editor__id code {
  display: block;
  width: 100%;
  padding-inline-end: 38px;
  overflow-wrap: anywhere;
  font-size: var(--text-sm);
}

.model-alias-editor__id-field {
  display: flex;
  width: 100%;
  min-height: var(--control-sm);
  max-width: 300px;
  align-items: center;
  padding-left: 6px;
}

.model-alias-editor__id-field :deep(.app-text-input) {
  width: 100%;
}

.model-alias-editor__alias-cell {
  display: grid;
  gap: var(--space-1);
}

.model-alias-editor__alias-field {
  width: 100%;
  min-width: 0;
}

.model-alias-editor__actions {
  display: flex;
  justify-content: flex-end;
}

.model-alias-editor__actions :deep(.icon-button:hover:not(:disabled)) {
  border-color: var(--color-danger);
  color: var(--color-danger);
}

.model-alias-editor__mobile-label {
  display: none;
  color: var(--color-text-faint);
  font-size: var(--text-label-xs);
  font-weight: 560;
}

.model-alias-editor__empty {
  grid-template-columns: minmax(0, 1fr);
  min-height: 64px;
  color: var(--color-text-faint);
  font-size: var(--text-sm);
  text-align: center;
}

.model-alias-editor__empty .ledger-record-list__cell {
  grid-column: 1 / -1;
}

.model-alias-editor__add {
  width: fit-content;
  min-height: 36px;
  justify-content: flex-start;
  margin-top: 10px;
  padding: 4px 1px;
  font-size: var(--text-sm);
  font-weight: 600;
}

@media (max-width: 860px) {
  .model-alias-editor__grid {
    --ledger-record-list-card-grid: minmax(0, 0.7fr) minmax(0, 1.3fr);
  }

  .model-alias-editor__record {
    padding-right: 58px;
  }

  .model-alias-editor__id,
  .model-alias-editor__alias-cell {
    grid-column: 1 / -1;
  }

  .model-alias-editor__id,
  .model-alias-editor__alias-cell,
  .model-alias-editor__claude-cell,
  .model-alias-editor__third-column {
    display: grid;
    align-content: start;
    gap: 5px;
  }

  /* 卡片模式下表头被隐藏，列位置完全由自动排布决定：不给开关列显式占满整行，它会和
     价格状态挤在同一行的两列里。 */
  .model-alias-editor__claude-cell {
    grid-column: 1 / -1;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 5px;
  }

  .model-alias-editor__claude-cell .model-alias-editor__mobile-label {
    flex-basis: 100%;
  }

  .model-alias-editor__alias-cell {
    border-top: 1px solid var(--color-border-subtle);
    padding-top: 11px;
  }

  .model-alias-editor__actions {
    position: absolute;
    top: 4px;
    right: 4px;
  }

  .model-alias-editor__actions :deep(.icon-button) {
    width: var(--touch-target);
    height: var(--touch-target);
  }

  .model-alias-editor__mobile-label {
    display: inline;
  }

  .model-alias-editor__id-field {
    min-height: var(--touch-target);
  }
}

@media (max-width: 640px) {
  .model-alias-editor__toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .model-alias-editor__search {
    width: 100%;
  }
}
</style>
