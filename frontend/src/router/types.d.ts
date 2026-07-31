import 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    /** Current functional menu label displayed as the page title. */
    title: string
    /** Menu hierarchy displayed in the workspace breadcrumb. */
    breadcrumbs: string[]
    /** Stable leaf key used to correlate routes with sidebar menus. */
    menuKey: string
  }
}

export {}
