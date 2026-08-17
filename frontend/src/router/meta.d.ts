import 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    breadcrumbs?: string[]
    menuKey?: string
    workspaceMode?: 'scroll' | 'contained'
    /** Whether the page must redirect access-token console sessions to the overview. */
    adminOnly?: boolean
  }
}
