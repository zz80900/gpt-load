/**
 * 模型别名的纯逻辑单源：Claude 适配开关推导、别名规范化、同名冲突检测。
 *
 * classic（features/models/model-draft 原地 re-export）与 modern（features/groups/
 * group-create-rules）共用这一份实现，任何一侧私自复制都会造成两端口径漂移。
 * 文件保持零依赖：参数类型就地声明为最小结构，不 import 任一前端的 api 类型。
 */

/** 别名规则的最小模型形状：classic 的 GroupModelUpdateDto 与 modern 的 ModelDraft 都与它结构兼容。 */
export interface ModelAliasDraft {
  id: string
  aliases?: string[]
}

/**
 * 「Claude 适配」开关写入的别名。
 *
 * 后端把它当作普通通配符别名处理（'*' 匹配任意长度，精确匹配优先），因此不存在需要
 * 与之对齐的后端常量：这里就是该字符串的唯一来源。
 */
export const claudeAdapterAlias = 'claude-*[1m]'

/**
 * 视为「已启用 Claude 适配」的别名。
 *
 * 上下文后缀 [1M] 与 [1m] 在服务端是等价的两个后缀（见 ContextSuffixes），用户在别名
 * 框里手输大写形态完全可能，因此两种写法都认；写入时一律用规范形态 claudeAdapterAlias。
 */
const claudeAdapterAliases: readonly string[] = [claudeAdapterAlias, 'claude-*[1M]']

/** 是否为 Claude 适配别名。展示、隐藏、回写、冲突呈现都只经由这个判定。 */
export function isClaudeAdapterAlias(alias: string): boolean {
  return claudeAdapterAliases.includes(alias)
}

/** 该模型是否已启用 Claude 适配。开关状态完全由别名推导，不额外存储。 */
export function hasClaudeAdapter(aliases: readonly string[]): boolean {
  return aliases.some(isClaudeAdapterAlias)
}

/**
 * 别名输入框与表格搜索应该展示的别名：只隐藏 Claude 适配常量。
 *
 * 刻意不隐藏一般通配符别名：用户手输的 gpt-* 若在输入框里「输入即消失」，既看不到也
 * 删不掉，而它仍然会被保存下来。
 */
export function visibleAliases(aliases: readonly string[]): string[] {
  return aliases.filter((alias) => !isClaudeAdapterAlias(alias))
}

/**
 * 按开关状态重写别名列表：开启时补齐规范形态（若已有大写形态则原样保留，不制造重复），
 * 关闭时移除全部形态。
 *
 * 始终返回新数组——GroupModelsTab 的 saved 与 draft 共享同一个 aliases 数组引用，就地
 * 改写会污染脏检查基线，使「放弃修改」无法恢复。
 */
export function withClaudeAdapter(aliases: readonly string[], enabled: boolean): string[] {
  if (!enabled) {
    return aliases.filter((alias) => !isClaudeAdapterAlias(alias))
  }
  if (hasClaudeAdapter(aliases)) {
    return [...aliases]
  }
  return [...aliases, claudeAdapterAlias]
}

/**
 * 规范化别名列表：逐项 trim、丢弃空项与等于模型 ID 的项、按首次出现去重（大小写敏感的
 * 精确比较）。必须与服务端 normalizeModelAliases 同规则，否则前端判为合法的配置会被
 * 服务端拒绝，用户会看到反复重试仍保存失败。
 *
 * 通配符别名不做形态判断，原样通过：它是普通别名，展开与过滤都发生在服务端与其他展示点。
 */
export function normalizeAliases(values: readonly string[], id: string): string[] {
  const trimmedID = id.trim()
  const aliases: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const alias = value.trim()
    if (alias === '' || alias === trimmedID || seen.has(alias)) continue
    seen.add(alias)
    aliases.push(alias)
  }
  return aliases
}

/** 前缀别名的前缀，与后端 control.claudePrefix 同源。 */
const claudePrefix = 'claude-'

/** 上下文后缀，与后端 modelname.ContextSuffixes 同形。 */
const contextSuffixes = ['[1M]', '[1m]']

/** 服务端别名长度上限，与后端 group_write.go 的 maxModelNameBytes 一致。 */
const maxModelNameBytes = 255

/**
 * 同步新增模型要补的 claude- 前缀别名；不适用时返回 null。
 *
 * 与后端 claudePrefixedName 同规则：空 ID、已带 claude- 前缀（大小写不敏感）、以上下文后缀
 * 结尾（带后缀的名字会额外认领基名，前缀叠加后撞名的概率不可控）、超过长度上限都不生成。
 */
export function claudePrefixedAlias(id: string): string | null {
  const trimmed = id.trim()
  if (trimmed === '' || trimmed.toLowerCase().startsWith(claudePrefix)) return null
  if (contextSuffixes.some((suffix) => trimmed.endsWith(suffix))) return null
  const alias = `${claudePrefix}${trimmed}`
  if (new TextEncoder().encode(alias).length > maxModelNameBytes) return null
  return alias
}

/**
 * 给同步新增的行生成 claude- 别名：名称被组内其它行认领时该行退回空别名。
 *
 * 认领集合取现有行与全部新增行的 ID、规范化别名（与 findModelNameConflicts 同口径），
 * 先把新增行自己的 ID 也算进去，因此新别名不会与同批新增的模型撞名，也不会凭空制造后端
 * 写路径的 MODEL_NAME_CONFLICT。
 *
 * 始终返回新数组、不就地改写行对象：对话框里的行与面板草稿共享引用，就地改写会污染脏检查
 * 基线（同 withClaudeAdapter 的既有教训）。
 */
export function withClaudePrefixedAliases<T extends ModelAliasDraft>(
  rows: readonly ModelAliasDraft[],
  additions: readonly T[],
): (T & { aliases: string[] })[] {
  const claimed = new Set<string>()
  for (const row of [...rows, ...additions]) {
    const id = row.id.trim()
    if (id === '') continue
    claimed.add(id)
    for (const alias of normalizeAliases(row.aliases ?? [], id)) claimed.add(alias)
  }
  return additions.map((addition) => {
    const alias = claudePrefixedAlias(addition.id)
    return { ...addition, aliases: alias !== null && !claimed.has(alias) ? [alias] : [] }
  })
}

export interface ModelNameConflict {
  client_model: string
  indexes: number[]
}

/** 与 classic 的 normalizeModel 同逻辑的内部形态：空 ID 的行不认领任何名称。 */
function normalizeModel(model: ModelAliasDraft): { id: string; aliases: string[] } | undefined {
  const id = model.id.trim()
  if (!id) return undefined
  return { id, aliases: normalizeAliases(model.aliases ?? [], id) }
}

/** Client names are intentionally exact and case sensitive, matching the API contract. */
export function findModelNameConflicts(models: readonly ModelAliasDraft[]): ModelNameConflict[] {
  // 名称 -> 认领它的上游 ID -> 该上游首次出现的条目下标。同一个上游的多条记录
  // （存量写法）可以共用名字；只有跨上游认领同名才是冲突——那会让该名称解析到
  // 两个不同上游，路由结果由候选排序而非配置决定。
  const ownersByName = new Map<string, Map<string, number>>()
  const nameOrder: string[] = []
  for (const [index, model] of models.entries()) {
    const normalized = normalizeModel(model)
    if (normalized === undefined) continue
    for (const name of [normalized.id, ...normalized.aliases]) {
      let owners = ownersByName.get(name)
      if (owners === undefined) {
        owners = new Map<string, number>()
        ownersByName.set(name, owners)
        nameOrder.push(name)
      }
      if (!owners.has(normalized.id)) owners.set(normalized.id, index)
    }
  }
  const conflicts: ModelNameConflict[] = []
  for (const name of nameOrder) {
    const owners = ownersByName.get(name)
    if (owners === undefined || owners.size < 2) continue
    conflicts.push({
      client_model: name,
      indexes: [...owners.values()].sort((left, right) => left - right),
    })
  }
  return conflicts
}

export function indexesWithConflicts(conflicts: readonly ModelNameConflict[]): Set<number> {
  return new Set(conflicts.flatMap((conflict) => conflict.indexes))
}

export function indexesWithEmptyIDs(models: readonly ModelAliasDraft[]): Set<number> {
  return new Set(models.flatMap((model, index) => (!model.id.trim() ? [index] : [])))
}

export function modelDraftValidity(
  models: readonly ModelAliasDraft[],
  conflicts: readonly ModelNameConflict[] = findModelNameConflicts(models),
): {
  conflictIndexes: Set<number>
  emptyIDIndexes: Set<number>
  invalidIndexes: Set<number>
} {
  const conflictIndexes = indexesWithConflicts(conflicts)
  const emptyIDIndexes = indexesWithEmptyIDs(models)
  return {
    conflictIndexes,
    emptyIDIndexes,
    invalidIndexes: new Set([...conflictIndexes, ...emptyIDIndexes]),
  }
}
