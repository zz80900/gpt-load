/**
 * 通配符模型名的判定与过滤。
 *
 * 服务端的 modelname.IsPattern 是这条规则的唯一定义：出现 '*' 即模式。前端凡是需要
 * 「客户端可用的具体模型名」的地方（选择器、自动补全、客户端配置生成、表格渲染）都必须
 * 经由本模块过滤，否则会把模式串当成真实模型名展示出去。
 */

/** 是否为通配符模型名。 */
export function isWildcardModelName(name: string): boolean {
  return name.includes('*')
}

/**
 * 过滤掉通配符模式，返回可枚举的具体模型名。
 *
 * 顺序与去重策略交给调用方：这里只做形态过滤，不改变相对顺序。
 */
export function concreteModelNames(names: readonly string[]): string[] {
  return names.filter((name) => !isWildcardModelName(name))
}
