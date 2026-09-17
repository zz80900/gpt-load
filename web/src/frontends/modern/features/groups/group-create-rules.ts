import { readModelAliases, type GroupChannel, type ModelDraft } from '@modern/api/group-create'

export interface GroupDraftModel extends ModelDraft {
  key: number
  origin: 'manual' | 'discovery' | 'configured'
}
// modelErrors 复刻后端 validateGroupCollectionModels 的冲突口径：名称 → 认领它的
// 上游模型 ID。同一 ID 的多行允许重复认领同一个名字（存量单别名写法借它表达
// 「一个模型两个名」），只有跨 ID 认领同名才算冲突——那会让该名称解析到两个上游。
export function modelErrors(models: readonly GroupDraftModel[]): Map<number, 'id' | 'duplicate'> {
  const claimed = new Map<string, string>()
  const errors = new Map<number, 'id' | 'duplicate'>()
  for (const model of models) {
    const id = model.id.trim()
    if (!id) {
      errors.set(model.key, 'id')
      continue
    }
    for (const name of [id, ...readModelAliases(model.alias)]) {
      const owner = claimed.get(name)
      if (owner === undefined) claimed.set(name, id)
      else if (owner !== id) errors.set(model.key, 'duplicate')
    }
  }
  return errors
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
