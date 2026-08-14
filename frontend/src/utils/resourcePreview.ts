export type ResourcePreviewMode = 'image' | 'pdf' | 'audio' | 'video' | 'text' | null

export interface ResourcePreviewInput {
  /** Resource category assigned by the request payload detector. */
  kind?: string
  /** Stored MIME type, when the upstream payload supplied one. */
  mimeType?: string
  /** Resource URI or data URI used as the browser preview source. */
  uri?: string
  /** Bounded data URI retained for image resources. */
  previewData?: string
}

function mimeFromDataUri(uri: string): string {
  const match = /^data:([^;,\s]+)/i.exec(uri)
  return match?.[1]?.toLowerCase() ?? ''
}

function extensionFromUri(uri: string): string {
  if (uri.startsWith('data:')) return ''
  try {
    const pathname = new URL(uri, window.location.origin).pathname
    return pathname.slice(pathname.lastIndexOf('.') + 1).toLowerCase()
  } catch {
    return ''
  }
}

/** Returns the safest native browser preview mode for a stored resource. */
export function resourcePreviewMode(resource: ResourcePreviewInput): ResourcePreviewMode {
  if (resource.kind === 'code') return null
  const source = resource.previewData || resource.uri || ''
  if (!source) return null
  if (resource.kind === 'file' && !/^(?:https?:|data:)/i.test(source)) return null
  if (!resource.previewData && source.startsWith('data:')) return null
  const mime = (resource.mimeType || mimeFromDataUri(source)).toLowerCase().split(';', 1)[0].trim()
  if (mime.startsWith('image/')) return 'image'
  if (mime === 'application/pdf') return 'pdf'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime.startsWith('video/')) return 'video'
  if (mime.startsWith('text/') || ['application/json', 'application/xml', 'application/javascript'].includes(mime)) return 'text'

  const extension = extensionFromUri(source)
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'avif', 'bmp', 'ico'].includes(extension)) return 'image'
  if (extension === 'pdf') return 'pdf'
  if (['mp3', 'wav', 'ogg', 'm4a', 'aac', 'flac'].includes(extension)) return 'audio'
  if (['mp4', 'webm', 'mov', 'm4v', 'ogv'].includes(extension)) return 'video'
  if (['txt', 'text', 'csv', 'log', 'md', 'json', 'xml'].includes(extension)) return 'text'
  return null
}

export function resourcePreviewSource(resource: ResourcePreviewInput): string {
  if (resource.previewData) return resource.previewData
  // URI is intentionally bounded for storage/display. Never feed a truncated
  // data URI to an <img>, otherwise the browser renders a blank preview.
  if (resource.uri?.startsWith('data:')) return ''
  return resource.uri || ''
}

/** Human-readable fallback that never dumps a long data URI into the UI. */
export function resourceReferenceLabel(uri?: string): string {
  if (!uri) return '无外部引用地址'
  if (uri.startsWith('data:')) return '内嵌内容未保留完整，暂不可预览'
  return uri
}

export function resourceExternalHref(uri?: string): string {
  if (!uri) return ''
  try {
    const parsed = new URL(uri, window.location.origin)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : ''
  } catch {
    return ''
  }
}

export function resourceFallbackLabel(resource: ResourcePreviewInput): string {
  if (resource.kind === 'skill') return '已识别 Skill 调用；该请求未携带可预览的 SKILL.md 正文'
  if (resource.uri?.startsWith('data:')) return '内嵌内容未保留完整，无法恢复预览'
  if (resource.uri?.startsWith('file-') || resource.uri?.startsWith('file_')) return '仅有文件标识，当前没有可下载地址'
  const mime = resource.mimeType?.toLowerCase().split(';', 1)[0].trim() ?? ''
  if (resource.kind === 'file' && (mime.startsWith('text/') || ['application/json', 'application/xml', 'application/yaml'].includes(mime))) return '请求中只包含源码路径，没有可高亮的文件内容'
  return '浏览器不支持此资源的内嵌预览'
}
