import type { GroupChannel, ModelDraft } from '@modern/api/group-create'
import {
  findModelNameConflicts,
  hasClaudeAdapter,
  indexesWithEmptyIDs,
  isClaudeAdapterAlias,
  normalizeAliases,
  withClaudeAdapter,
} from '@shared/models/model-aliases'

export interface GroupDraftModel extends ModelDraft {
  key: number
  origin: 'manual' | 'discovery' | 'configured'
}

/** 错误归属的列：ID 输入、别名 tag 输入或 Claude 适配开关。 */
export type ModelColumn = 'id' | 'alias' | 'claude'

/** 一行草稿的首个校验错误：column 是归属列，name 是冲突的对外名称（空 ID 错误时为空）。 */
export interface ModelDraftError {
  column: ModelColumn
  name: string
}

// modelErrors 经 @shared 的 findModelNameConflicts 复刻后端 validateGroupCollectionModels
// 的冲突口径：同一上游 ID 的多行可重复认领同一名称（存量单别名写法借它表达「一个模型
// 两个名」），只有跨 ID 认领同名才是冲突。列归属与 classic ModelAliasEditor 一致：空 ID
// 或冲突名等于行 ID → ID 列；冲突名是 Claude 适配常量 → 开关列；其余 → 别名列。
// 一行只报首个命中。
export function modelErrors(models: readonly GroupDraftModel[]): Map<number, ModelDraftError> {
  const errors = new Map<number, ModelDraftError>()
  for (const index of indexesWithEmptyIDs(models)) {
    const model = models[index]
    if (model) errors.set(model.key, { column: 'id', name: '' })
  }
  for (const conflict of findModelNameConflicts(models)) {
    for (const index of conflict.indexes) {
      const model = models[index]
      if (!model || errors.has(model.key)) continue
      const name = conflict.client_model
      errors.set(model.key, {
        column: name === model.id.trim() ? 'id' : isClaudeAdapterAlias(name) ? 'claude' : 'alias',
        name,
      })
    }
  }
  return errors
}

/**
 * 按开关状态重写别名数组：开关不引入新的持久化字段，状态完全由别名推导。
 *
 * 开关在分组内互斥：打开一行会自动关闭其余全部，因此同一时刻只会有一个模型启用
 * Claude 适配。做成互斥而不是报错，是因为后端的同名模式冲突规则（同一分组内两个
 * 上游认领 claude-*[1m]）依然成立，但用户不该被迫先手工关掉另一个才能打开这一个。
 * 关闭某个开关只影响它自己。
 */
export function setClaudeAdapter(
  models: readonly GroupDraftModel[],
  key: number,
  enabled: boolean,
): GroupDraftModel[] {
  return models.map((model) => {
    if (model.key === key) return { ...model, aliases: withClaudeAdapter(model.aliases, enabled) }
    if (enabled && hasClaudeAdapter(model.aliases))
      return { ...model, aliases: withClaudeAdapter(model.aliases, false) }
    return model
  })
}

/**
 * 写回别名输入框发出的「可见」列表：开关状态由「该行当前状态或入参列表中出现常量」
 * 取「或」推导。取当前状态是为了编辑普通别名时不丢掉被隐藏的 claude-*[1m]；取入参
 * 是为了让手输 claude-*[1m] / claude-*[1M] 等价于打开开关（AC4）——否则开关关闭时
 * withClaudeAdapter(_, false) 会把手输的常量静默丢弃。最后按与服务端同规则的
 * normalizeAliases 规范化（trim、去空、去 ID、去重）。
 */
export function setAliases(
  models: readonly GroupDraftModel[],
  key: number,
  aliases: readonly string[],
): GroupDraftModel[] {
  return models.map((model) =>
    model.key === key
      ? {
          ...model,
          aliases: normalizeAliases(
            withClaudeAdapter(
              aliases,
              hasClaudeAdapter(aliases) || hasClaudeAdapter(model.aliases),
            ),
            model.id,
          ),
        }
      : model,
  )
}

export function credentialCount(raw: string, channel: GroupChannel | undefined): number {
  const value = raw.trim()
  if (!value) return 0
  if (
    channel &&
    (channel.credentialFields.length !== 1 || channel.credentialFields[0]?.key !== 'api_key') &&
    value.startsWith('{')
  ) {
    try {
      const parsed: unknown = JSON.parse(value)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return 1
    } catch {
      /* 多行 JSON 凭据继续按行统计，完整校验交给后端。 */
    }
  }
  return value.split(/\r?\n/u).filter((line) => line.trim()).length
}
export function validBaseURL(value: string): boolean {
  try {
    const url = new URL(value)
    return (
      ['http:', 'https:'].includes(url.protocol) &&
      Boolean(url.hostname) &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash &&
      !value.endsWith('?')
    )
  } catch {
    return false
  }
}
