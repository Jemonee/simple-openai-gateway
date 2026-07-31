import 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    breadcrumbs?: string[]
    menuKey?: string
    workspaceMode?: 'scroll' | 'contained'
  }
}
