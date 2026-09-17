<script setup lang="ts">
import { RefreshCw } from '@lucide/vue'
import { computed, onScopeDispose, ref } from 'vue'
import { useURLState } from '@modern/app/url-state'
import { useLoadingActivity } from '@modern/components/ui/loading'
import { useI18n } from 'vue-i18n'
import { discoverGroupModels } from '@modern/api/group-detail'
import type { ModelCandidate } from '@modern/api/model-discovery'
import { AppButton, AppConfirmDialog, AppNotice, AppSegmentedControl } from '@modern/components/ui'
import { useApiClient } from '@shared/http/client-context'
import { findModelNameConflicts, visibleAliases } from '@shared/models/model-aliases'
import type { GroupDraftModel } from './group-create-rules'

const props = defineProps<{ groupId: number; models: readonly GroupDraftModel[] }>()
const emit = defineEmits<{ close: []; confirm: [models: GroupDraftModel[]] }>()
const { t, n } = useI18n()
const client = useApiClient()
const current = props.models.map((model) => ({ ...model }))
const candidates = ref<ModelCandidate[]>()
const loading = ref(false)
const failed = ref(false)
const view = useURLState(
  ['sync_mode'],
  (query) => ({
    mode: query.sync_mode === 'add' || query.sync_mode === 'cleanup' ? query.sync_mode : 'full',
  }),
  (value) => (value.mode === 'full' ? {} : { sync_mode: value.mode }),
)
const mode = computed({
  get: () => view.value.mode,
  set: (mode: string) => {
    view.value = { mode }
  },
})
useLoadingActivity(loading)
let controller: AbortController | undefined
const modes = computed(() =>
  ['full', 'add', 'cleanup'].map((value) => ({
    value,
    label: t('groupWorkflows.syncModes.' + value),
  })),
)
const live = computed(() =>
  (candidates.value ?? []).filter((model) => model.sources.includes('live')),
)
const additions = computed(() =>
  mode.value === 'cleanup'
    ? []
    : live.value.filter((model) => !current.some((item) => item.id.trim() === model.id)),
)
const removals = computed(() =>
  mode.value === 'add'
    ? []
    : current.filter((model) => !live.value.some((item) => item.id === model.id.trim())),
)
// 合并结果保留每行的既有别名（含隐藏的 claude-*[1m]，开关状态随之保留），增补行无别名。
const next = computed<GroupDraftModel[]>(() => {
  const removed = new Set(removals.value.map((model) => model.key))
  let key = Math.max(-1, ...current.map((model) => model.key)) + 1
  return [
    ...current.filter((model) => !removed.has(model.key)),
    ...additions.value.map((model) => ({
      id: model.id,
      aliases: [],
      key: key++,
      origin: 'discovery' as const,
    })),
  ]
})
// 冲突口径与后端一致：跨上游认领同名才冲突，同一上游多行重复认领同名放行。
const conflicts = computed(() =>
  findModelNameConflicts(next.value).map((conflict) => conflict.client_model),
)
async function load(): Promise<void> {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  failed.value = false
  candidates.value = undefined
  try {
    const result = await discoverGroupModels(client, props.groupId, request.signal)
    if (!request.signal.aborted) candidates.value = result
  } catch {
    if (!request.signal.aborted) failed.value = true
  } finally {
    if (!request.signal.aborted) loading.value = false
  }
}
void load()
onScopeDispose(() => controller?.abort())
</script>

<template>
  <AppConfirmDialog
    open
    :icon="RefreshCw"
    :title="t('groupWorkflows.syncModels')"
    :description="t('groupWorkflows.syncDescription')"
    :confirm-label="t('groupWorkflows.confirmSync')"
    :disabled="
      loading || !candidates || Boolean(conflicts.length) || (!additions.length && !removals.length)
    "
    :tone="removals.length ? 'danger' : 'brand'"
    @cancel="emit('close')"
    @confirm="emit('confirm', next)"
  >
    <AppSegmentedControl
      v-model="mode"
      :label="t('groupWorkflows.syncMode')"
      :options="modes"
      :disabled="loading"
      appearance="field"
      size="sm"
    />
    <AppNotice v-if="loading">{{ t('collection.loading') }}</AppNotice>
    <AppNotice v-else-if="failed" tone="danger">
      {{ t('groupCreate.discoveryFailed') }}
      <template #actions
        ><AppButton size="sm" @click="load">{{ t('ui.retry') }}</AppButton></template
      >
    </AppNotice>
    <template v-else-if="candidates">
      <div class="modern-model-sync-changes">
        <section class="modern-model-sync-additions">
          <h3>{{ t('groupWorkflows.additions', { count: n(additions.length) }) }}</h3>
          <ul>
            <li v-for="model in additions" :key="model.id">{{ model.id }}</li>
          </ul>
        </section>
        <section class="modern-model-sync-removals">
          <h3>{{ t('groupWorkflows.removals', { count: n(removals.length) }) }}</h3>
          <ul>
            <li v-for="model in removals" :key="model.key">
              {{ visibleAliases(model.aliases)[0] ?? model.id }}
            </li>
          </ul>
        </section>
      </div>
      <AppNotice v-if="conflicts.length" tone="danger"
        >{{ t('groupWorkflows.modelConflicts') }} {{ conflicts.join('、') }}</AppNotice
      >
      <AppNotice v-else-if="!additions.length && !removals.length">{{
        t('groupWorkflows.noModelChanges')
      }}</AppNotice>
      <AppNotice v-else-if="!next.length" tone="warning">{{
        t('groupWorkflows.clearModels')
      }}</AppNotice>
    </template>
  </AppConfirmDialog>
</template>

<style scoped>
.modern-model-sync-changes {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: var(--modern-space-3);
}
.modern-model-sync-changes section {
  padding: var(--modern-space-3);
  border-radius: var(--modern-radius-control);
}
.modern-model-sync-additions {
  background: var(--modern-success-soft);
  color: var(--modern-success);
}
.modern-model-sync-removals {
  background: var(--modern-danger-soft);
  color: var(--modern-danger);
}
.modern-model-sync-changes h3 {
  font-size: var(--modern-font-size-secondary);
  font-weight: var(--modern-weight-medium);
}
.modern-model-sync-changes ul {
  max-height: 220px;
  overflow-y: auto;
  padding-top: var(--modern-space-2);
  font-size: var(--modern-font-size-small);
  overflow-wrap: anywhere;
}
.modern-model-sync-changes li + li {
  margin-top: var(--modern-space-1);
}
</style>
