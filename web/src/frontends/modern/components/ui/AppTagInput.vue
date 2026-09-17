<script setup lang="ts">
import { X } from '@lucide/vue'
import { computed, nextTick, ref, useId } from 'vue'

import AppIcon from './AppIcon.vue'

defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    modelValue: string[]
    label: string
    placeholder?: string
    disabled?: boolean
    invalid?: boolean
    describedBy?: string
    /** 生成标签删除按钮的 aria-label。 */
    removeLabel: (tag: string) => string
    /** 触发提交的分隔符；输入这些字符等同于按回车。 */
    separators?: readonly string[]
  }>(),
  {
    placeholder: undefined,
    disabled: false,
    invalid: false,
    describedBy: undefined,
    separators: () => [',', '，', '、'],
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: string[]]
  blur: []
}>()

const inputId = useId()
const input = ref<HTMLInputElement>()
const draft = ref('')

const hasTags = computed(() => props.modelValue.length > 0)

/** 追加一批标签：trim、丢弃空项、对自身集合去重。返回是否发生变更。 */
function appendTags(values: readonly string[]): boolean {
  const next = [...props.modelValue]
  const seen = new Set(next)
  let changed = false
  for (const value of values) {
    const tag = value.trim()
    if (tag === '' || seen.has(tag)) continue
    seen.add(tag)
    next.push(tag)
    changed = true
  }
  if (changed) emit('update:modelValue', next)
  return changed
}

/** 提交输入框中的完整内容（失焦、回车、分隔符都走这里）。 */
function commitDraft(): boolean {
  if (draft.value === '') return false
  const value = draft.value
  draft.value = ''
  return appendTags([value])
}

function removeTag(tag: string): void {
  emit(
    'update:modelValue',
    props.modelValue.filter((value) => value !== tag),
  )
  void nextTick(() => input.value?.focus())
}

function onInput(event: Event): void {
  const value = (event.target as HTMLInputElement).value
  const separator = props.separators.find((candidate) => value.includes(candidate))
  if (separator === undefined) {
    draft.value = value
    return
  }
  // 输入分隔符时直接提交，且不把分隔符本身留在输入框里。
  const parts = value.split(separator)
  const tail = parts.pop() ?? ''
  const changed = appendTags(parts)
  draft.value = tail
  if (!changed && tail === '') draft.value = ''
  if (input.value) input.value.value = tail
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter') {
    event.preventDefault()
    commitDraft()
    return
  }
  // 输入框为空时退格删除最后一个标签；不把标签文本还原进输入框——对粘贴产生的
  // 长标签来说那很反直觉。
  if (event.key === 'Backspace' && draft.value === '' && hasTags.value) {
    event.preventDefault()
    const last = props.modelValue[props.modelValue.length - 1]
    if (last !== undefined) emit('update:modelValue', props.modelValue.slice(0, -1))
  }
}

function onPaste(event: ClipboardEvent): void {
  const text = event.clipboardData?.getData('text') ?? ''
  if (text === '') return
  const pattern = new RegExp(`[${props.separators.map(escapeForClass).join('')}\\n\\t]`)
  if (!pattern.test(text)) return // 不含分隔符时放行浏览器默认粘贴
  event.preventDefault()
  const parts = text.split(pattern)
  const changed = appendTags(parts)
  const tail = draft.value
  draft.value = ''
  if (!changed && tail === '') return
  if (input.value) input.value.value = ''
}

function escapeForClass(value: string): string {
  return value.replace(/[\\\]^]/gu, '\\$&')
}

function onBlur(): void {
  // 失焦时先提交未提交的文本，否则用户输入的最后一段会被静默丢弃。
  commitDraft()
  emit('blur')
}

defineExpose({ focus: () => input.value?.focus() })
</script>

<template>
  <div
    class="modern-tag-input"
    :class="{ 'is-invalid': invalid, 'is-disabled': disabled }"
  >
    <label class="modern-sr-only" :for="inputId">{{ label }}</label>
    <span v-for="tag in modelValue" :key="tag" class="modern-tag-input__tag">
      <span class="modern-tag-input__tag-label">{{ tag }}</span>
      <button
        type="button"
        class="modern-tag-input__remove"
        :disabled="disabled"
        :aria-label="removeLabel(tag)"
        @click="removeTag(tag)"
      >
        <AppIcon :icon="X" size="xs" />
      </button>
    </span>
    <input
      :id="inputId"
      ref="input"
      v-bind="$attrs"
      class="modern-tag-input__field"
      type="text"
      autocomplete="off"
      :spellcheck="false"
      :value="draft"
      :disabled="disabled"
      :placeholder="hasTags ? undefined : placeholder"
      :aria-invalid="invalid || undefined"
      :aria-describedby="describedBy"
      @input="onInput"
      @keydown="onKeydown"
      @paste="onPaste"
      @blur="onBlur"
    />
  </div>
</template>

<style scoped>
.modern-tag-input {
  display: flex;
  min-width: 0;
  min-height: var(--modern-control-sm);
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-1);
  border: var(--modern-line-width) solid var(--modern-control-border);
  border-radius: var(--modern-radius-control);
  background: var(--modern-surface);
  padding: var(--modern-space-1) var(--modern-space-2);
  color: var(--modern-text);
  font-size: var(--modern-font-size-secondary);
  line-height: var(--modern-leading-compact);
  box-shadow: var(--modern-shadow-control);
  cursor: text;
}

.modern-tag-input:focus-within {
  border-color: var(--modern-accent);
  box-shadow: var(--modern-shadow-focus);
}

.modern-tag-input.is-invalid {
  border-color: var(--modern-danger);
}

.modern-tag-input.is-invalid:focus-within {
  box-shadow: var(--modern-shadow-focus-danger);
}

.modern-tag-input.is-disabled {
  background: var(--modern-control-disabled);
  box-shadow: none;
  cursor: not-allowed;
}

.modern-tag-input__tag {
  display: inline-flex;
  max-width: 100%;
  min-height: var(--modern-badge-xs);
  align-items: center;
  gap: var(--modern-space-0-5);
  border: var(--modern-line-width) solid var(--modern-tooltip-border);
  border-radius: var(--modern-radius-small);
  background: var(--modern-subtle);
  padding: var(--modern-space-0-5);
  color: var(--modern-text);
  font-size: var(--modern-font-size-small);
}

.modern-tag-input__tag-label {
  min-width: 0;
  overflow: hidden;
  padding-left: var(--modern-space-1-5);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.modern-tag-input__remove {
  display: inline-flex;
  width: var(--modern-badge-xs);
  height: var(--modern-badge-xs);
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: var(--modern-radius-round);
  background: transparent;
  color: var(--modern-muted);
  padding: 0;
  cursor: pointer;
}

.modern-tag-input__remove:hover:not(:disabled) {
  background: var(--modern-danger-soft);
  color: var(--modern-danger);
}

.modern-tag-input__remove:focus-visible {
  outline: var(--modern-focus-width) solid var(--modern-accent);
  outline-offset: var(--modern-focus-offset);
}

.modern-tag-input__remove:disabled {
  cursor: not-allowed;
}

.modern-tag-input__field {
  min-width: 96px;
  flex: 1 1 96px;
  border: 0;
  background: transparent;
  color: var(--modern-text);
  padding: var(--modern-space-1) 0;
  font: inherit;
  font-size: var(--modern-font-size-secondary);
  line-height: var(--modern-leading-compact);
  outline: none;
}

.modern-tag-input__field::placeholder {
  color: var(--modern-control-placeholder);
  opacity: 1;
}

.modern-tag-input__field:disabled {
  color: var(--modern-muted);
  cursor: not-allowed;
}

@media (max-width: 760px) {
  .modern-tag-input {
    min-height: var(--modern-touch-target);
  }

  .modern-tag-input__field {
    font-size: var(--modern-font-size-input-mobile);
  }
}
</style>
