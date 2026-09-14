// 本应用只提供中文界面：locales/ 下的 en-US 与 ja-JP 保留在仓库中但不被加载。
// 保留的意义是让 git 能正常跟踪上游对这些文件的改动，避免每次合并上游都产生
// delete/modify 冲突；因为下方 loader 只注册 zh-CN，它们也不会进入构建产物。
import { createI18n } from 'vue-i18n'

export const appLocale = 'zh-CN' as const
export type AppLocale = typeof appLocale

type MessageTree = { [key: string]: string | MessageTree }
type MessageLoader = () => Promise<{ default: MessageTree }>

const coreLoader: MessageLoader = () => import('./locales/zh-CN/core')
const namespaceLoaders = {
  import: () => import('./locales/zh-CN/import'),
  group: () => import('./locales/zh-CN/group'),
  'access-keys': () => import('./locales/zh-CN/access-keys'),
  monitor: () => import('./locales/zh-CN/monitor'),
  models: () => import('./locales/zh-CN/models'),
  'model-prices': () => import('./locales/zh-CN/model-prices'),
  settings: () => import('./locales/zh-CN/settings'),
} satisfies Record<string, MessageLoader>

export type MessageNamespace = keyof typeof namespaceLoaders

function createI18nPlugin(messages: MessageTree) {
  return createI18n({
    legacy: false as const,
    locale: appLocale,
    messages: { [appLocale]: messages },
  })
}

export interface AppI18n {
  plugin: ReturnType<typeof createI18nPlugin>
  getLocale(): AppLocale
  loadNamespaces(requested: readonly MessageNamespace[]): Promise<void>
}

export async function createAppI18n(): Promise<AppI18n> {
  const plugin = createI18nPlugin((await coreLoader()).default)
  const loaded = new Set<string>(['core'])
  const pending = new Map<string, Promise<void>>()

  async function ensure(namespace: MessageNamespace): Promise<void> {
    if (loaded.has(namespace)) return
    const existing = pending.get(namespace)
    if (existing) return existing
    const request = namespaceLoaders[namespace]()
      .then((module) => {
        plugin.global.mergeLocaleMessage(appLocale, module.default)
        loaded.add(namespace)
      })
      .finally(() => pending.delete(namespace))
    pending.set(namespace, request)
    return request
  }

  document.documentElement.lang = appLocale
  return {
    plugin,
    getLocale() {
      return appLocale
    },
    async loadNamespaces(requested) {
      await Promise.all([...new Set(requested)].map((namespace) => ensure(namespace)))
    },
  }
}
