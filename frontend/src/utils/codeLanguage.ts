export interface SourceCodeResourceInput {
  /** Resource category assigned by the request detector. */
  kind?: string
  /** Display name or original filename. */
  name?: string
  /** Stored MIME type inferred from the payload or source filename. */
  mimeType?: string
  /** Original resource reference, usually a local path for source files. */
  uri?: string
  /** Bounded resource preview retained by the gateway. */
  preview?: string
}

export interface SourceCodePreviewValue {
  /** Bounded source text rendered by the syntax highlighter. */
  code: string
  /** Monaco language identifier used for tokenization. */
  language: string
}

const mimeLanguages: Record<string, string> = {
  'application/json': 'json',
  'application/xml': 'xml',
  'application/yaml': 'yaml',
  'text/css': 'css',
  'text/html': 'html',
  'text/javascript': 'javascript',
  'text/markdown': 'markdown',
  'text/typescript': 'typescript',
  'text/x-c': 'cpp',
  'text/x-c++': 'cpp',
  'text/x-csharp': 'csharp',
  'text/x-dart': 'dart',
  'text/x-diff': 'plaintext',
  'text/x-dockerfile': 'dockerfile',
  'text/x-go': 'go',
  'text/x-java-source': 'java',
  'text/x-kotlin': 'kotlin',
  'text/x-less': 'less',
  'text/x-lua': 'lua',
  'text/x-php': 'php',
  'text/x-python': 'python',
  'text/x-ruby': 'ruby',
  'text/x-rust': 'rust',
  'text/x-scala': 'scala',
  'text/x-scss': 'scss',
  'text/x-shellscript': 'shell',
  'text/x-sql': 'sql',
  'text/x-swift': 'swift',
  'text/x-vue': 'html',
}

const extensionLanguages: Record<string, string> = {
  bash: 'shell', c: 'cpp', cc: 'cpp', cjs: 'javascript', cpp: 'cpp', cs: 'csharp', css: 'css',
  cts: 'typescript', cxx: 'cpp', dart: 'dart', fish: 'shell', go: 'go', h: 'cpp', hpp: 'cpp',
  htm: 'html', html: 'html', hxx: 'cpp', java: 'java', js: 'javascript', json: 'json', jsonc: 'json',
  jsx: 'javascript', kt: 'kotlin', kts: 'kotlin', less: 'less', lua: 'lua', markdown: 'markdown', md: 'markdown',
  mjs: 'javascript', mts: 'typescript', php: 'php', py: 'python', pyw: 'python', rb: 'ruby', rs: 'rust',
  scala: 'scala', scss: 'scss', sh: 'shell', sql: 'sql', swift: 'swift', ts: 'typescript', tsx: 'typescript',
  vue: 'html', xml: 'xml', yaml: 'yaml', yml: 'yaml', zsh: 'shell',
}

const languageLabels: Record<string, string> = {
  cpp: 'C/C++', csharp: 'C#', css: 'CSS', dart: 'Dart', dockerfile: 'Dockerfile', go: 'Go', html: 'HTML/Vue',
  java: 'Java', javascript: 'JavaScript', json: 'JSON', kotlin: 'Kotlin', less: 'Less', lua: 'Lua',
  markdown: 'Markdown', php: 'PHP', plaintext: '代码', python: 'Python', ruby: 'Ruby', rust: 'Rust',
  scala: 'Scala', scss: 'SCSS', shell: 'Shell', sql: 'SQL', swift: 'Swift', typescript: 'TypeScript',
  xml: 'XML', yaml: 'YAML',
}

function extensionFromReference(value: string): string {
  const clean = value.split(/[?#]/, 1)[0].replace(/:\d+(?::\d+)?$/, '').replaceAll('\\', '/')
  const base = clean.slice(clean.lastIndexOf('/') + 1).toLowerCase()
  if (base === 'dockerfile') return 'dockerfile'
  const dot = base.lastIndexOf('.')
  return dot >= 0 ? base.slice(dot + 1) : ''
}

/** Resolves a Monaco language from MIME metadata first, then the source filename. */
export function sourceCodeLanguage(resource: SourceCodeResourceInput): string | null {
  const mime = resource.mimeType?.toLowerCase().split(';', 1)[0].trim() ?? ''
  if (mimeLanguages[mime]) return mimeLanguages[mime]
  const extension = extensionFromReference(resource.name || resource.uri || '')
  return extensionLanguages[extension] ?? (resource.kind === 'code' ? 'plaintext' : null)
}

/** Returns source text only when the resource retained actual code rather than just a path. */
export function sourceCodePreview(resource: SourceCodeResourceInput): SourceCodePreviewValue | null {
  const language = sourceCodeLanguage(resource)
  const preview = resource.preview?.trim() ?? ''
  if (!language || !preview) return null
  if (resource.kind === 'file' && (preview === resource.uri?.trim() || preview === resource.name?.trim())) return null
  return { code: preview, language }
}

/** Human-readable language name shown above a highlighted source preview. */
export function sourceCodeLanguageLabel(language: string): string {
  return languageLabels[language] ?? language
}
