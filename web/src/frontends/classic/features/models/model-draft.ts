import type { ModelPricingStatus } from '@/api/control/types'
import type { GroupModelUpdateDto } from '@/app/resources/groups'
import type { ModelCandidate, ModelCandidateSource } from '@/app/resources/providers'

import { normalizeAliases } from '@shared/models/model-aliases'
import type { ModelNameConflict } from '@shared/models/model-aliases'

// 别名 / Claude 适配 / 冲突检测的纯逻辑已单源化到 @shared/models/model-aliases，
// classic 与 modern 共用；此处原地 re-export，全部调用点继续从本文件导入。
export {
  claudeAdapterAlias,
  isClaudeAdapterAlias,
  hasClaudeAdapter,
  visibleAliases,
  withClaudeAdapter,
  normalizeAliases,
  findModelNameConflicts,
  indexesWithConflicts,
  indexesWithEmptyIDs,
  modelDraftValidity,
} from '@shared/models/model-aliases'
export type { ModelNameConflict } from '@shared/models/model-aliases'

export type ModelDraftKey = string | number

export interface ModelDraftValue extends GroupModelUpdateDto {
  key: ModelDraftKey
  editable_id?: boolean
  name: string
  sources: ModelCandidateSource[]
  pricing_status: ModelPricingStatus
}

export interface ModelAliasEditorLabels {
  tableLabel: string
  id: string
  alias: string
  thirdColumn: string
  actions: string
  search: string
  searchLabel: string
  clearSearch: string
  aliasFor: (id: string) => string
  aliasPlaceholder: string
  removeAliasFor: (alias: string) => string
  removeFor: (id: string) => string
  manualId: string
  manualIdRequired: string
  add: string
  addInline: string
  count: (count: number) => string
  empty: string
  noMatches: string
  nameConflict: (name: string) => string
}

export interface ModelDiscoveryDrawerLabels {
  title: string
  description: string
  close: string
  loading: string
  search: string
  clearSearch: string
  filterLabel: string
  filterUnadded: string
  filterAll: string
  alreadyAdded: string
  unadded: string
  noMatches: string
  empty: string
  selected: (count: number) => string
  selectAll: string
  deselectAll: string
  retry: string
  cancel: string
  confirm: string
  pricingStatus: Record<ModelPricingStatus, string>
  pricingDiscovered: (source: string) => string
  sources: Record<ModelCandidateSource, string>
}

export function mergeCandidateMetadata<T extends ModelDraftValue>(
  draft: readonly T[],
  candidates: readonly ModelCandidate[],
): T[] {
  const byID = new Map(candidates.map((candidate) => [candidate.id, candidate] as const))
  return draft.map((item) => {
    const candidate = byID.get(item.id.trim())
    return candidate
      ? ({
          ...item,
          name: candidate.name,
          sources: [...candidate.sources],
          pricing_status: candidate.pricing_status,
        } as T)
      : ({ ...item, sources: [...item.sources] } as T)
  })
}

export function appendSelectedCandidates<T extends ModelDraftValue>(
  draft: readonly T[],
  selected: readonly ModelCandidate[],
  create: (candidate: ModelCandidate) => T,
): T[] {
  const result = mergeCandidateMetadata(draft, selected)
  const present = new Set(result.map(({ id }) => id.trim()).filter(Boolean))
  for (const candidate of selected) {
    if (present.has(candidate.id)) continue
    present.add(candidate.id)
    result.push(create(candidate))
  }
  return result
}

export function readModelNameConflicts(value: unknown): ModelNameConflict[] {
  if (typeof value !== 'object' || value === null || !('conflicts' in value)) return []
  const conflicts = (value as { conflicts?: unknown }).conflicts
  if (!Array.isArray(conflicts)) return []

  return conflicts.flatMap((item) => {
    if (
      typeof item !== 'object' ||
      item === null ||
      typeof (item as { client_model?: unknown }).client_model !== 'string' ||
      !Array.isArray((item as { indexes?: unknown }).indexes) ||
      !(item as { indexes: unknown[] }).indexes.every(
        (index) => typeof index === 'number' && Number.isSafeInteger(index) && index >= 0,
      )
    ) {
      return []
    }
    return [item as ModelNameConflict]
  })
}

export function normalizeModel(model: GroupModelUpdateDto): GroupModelUpdateDto | undefined {
  const id = model.id.trim()
  if (!id) return undefined
  return { id, aliases: normalizeAliases(model.aliases ?? [], id) }
}

/** 客户端可用的全部名称：上游 ID 在首位，其后是规范化后的别名。 */
export function clientModels(model: GroupModelUpdateDto): string[] {
  const normalized = normalizeModel(model)
  return normalized === undefined ? [] : [normalized.id, ...normalized.aliases]
}

export function createModelDraft<T extends GroupModelUpdateDto>(
  items: readonly T[],
): Array<T & { key: number }>
export function createModelDraft<T extends GroupModelUpdateDto, K extends ModelDraftKey>(
  items: readonly T[],
  createKey: (item: T, index: number) => K,
): Array<T & { key: K }>
export function createModelDraft<T extends GroupModelUpdateDto, K extends ModelDraftKey>(
  items: readonly T[],
  createKey?: (item: T, index: number) => K,
): Array<T & { key: K | number }> {
  return items.map((item, index) => ({
    ...item,
    key: createKey ? createKey(item, index) : index,
  }))
}

export function normalizedModels(draft: readonly GroupModelUpdateDto[]): GroupModelUpdateDto[] {
  return draft.flatMap((item) => {
    const normalized = normalizeModel(item)
    return normalized === undefined ? [] : [normalized]
  })
}

export function sameModels(
  left: readonly GroupModelUpdateDto[],
  right: readonly GroupModelUpdateDto[],
): boolean {
  return JSON.stringify(normalizedModels(left)) === JSON.stringify(normalizedModels(right))
}
