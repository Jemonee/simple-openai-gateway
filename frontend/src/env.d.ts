declare const __BUILD_BRANCH__: string

declare module '*.vue' {
    import type { DefineComponent } from 'vue'
    const component: DefineComponent<{}, {}, any>
    export default component
}

declare module '@/config/project.generated.js' {
    interface ProjectMeta {
        appName: string
        displayName: string
        description: string
        version: string
        faviconPath: string
    }
    const projectMeta: ProjectMeta
    export default projectMeta
}
