export type ConversationRole = 'user' | 'assistant' | 'system' | 'developer' | 'tool' | 'error'

export interface ConversationMessage {
  id: string
  role: ConversationRole
  label: string
  content: string
}

export interface PayloadDeltaMetadata {
  version: number
  mode: 'session' | 'attempt'
  baseRequestId: string
  omittedFields?: string[]
  omittedItems?: Record<string, number>
  removedFields?: string[]
}

interface StoredPayload {
  payload: Record<string, unknown> | null
  delta: PayloadDeltaMetadata | null
}

const roleLabels: Record<ConversationRole, string> = {
  user: '用户',
  assistant: 'AI',
  system: '系统',
  developer: '开发者',
  tool: '工具',
  error: '错误',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function asRole(value: unknown): ConversationRole {
  if (value === 'user' || value === 'assistant' || value === 'system' || value === 'developer' || value === 'tool') return value
  return 'assistant'
}

function textContent(value: unknown): string {
  if (typeof value === 'string') return value
  if (Array.isArray(value)) return value.map(textContent).filter(Boolean).join('\n')
  if (!isRecord(value)) return ''
  if (typeof value.text === 'string') return value.text
  if (typeof value.value === 'string') return value.value
  if (typeof value.output_text === 'string') return value.output_text
  if (value.content !== undefined) return textContent(value.content)
  return ''
}

function formattedJSON(value: unknown): string {
  try {
    return `\`\`\`json\n${JSON.stringify(value, null, 2)}\n\`\`\``
  } catch {
    return String(value)
  }
}

function toolCallContent(value: unknown): string {
  if (!Array.isArray(value) || value.length === 0) return ''
  return value.map((item) => {
    if (!isRecord(item)) return formattedJSON(item)
    const fn = isRecord(item.function) ? item.function : item
    const name = typeof fn.name === 'string' ? fn.name : '未命名工具'
    const args = typeof fn.arguments === 'string' ? fn.arguments : JSON.stringify(fn.arguments ?? {}, null, 2)
    return `调用工具 **${name}**\n\n\`\`\`json\n${args}\n\`\`\``
  }).join('\n\n')
}

function createMessage(role: ConversationRole, content: string, index: number, label?: string): ConversationMessage | null {
  const trimmed = content.trim()
  if (!trimmed) return null
  return { id: `${role}-${index}`, role, label: label ?? roleLabels[role], content: trimmed }
}

export function parseStoredPayload(value: string): StoredPayload {
  const trimmed = value.trim()
  if (!trimmed) return { payload: null, delta: null }
  try {
    const parsed: unknown = JSON.parse(trimmed)
    if (!isRecord(parsed)) return { payload: null, delta: null }
    if (isRecord(parsed._gatewayLog) && isRecord(parsed.payload)) {
      return {
        payload: parsed.payload,
        delta: parsed._gatewayLog as unknown as PayloadDeltaMetadata,
      }
    }
    return { payload: parsed, delta: null }
  } catch {
    return { payload: null, delta: null }
  }
}

function messageFromItem(item: unknown, index: number): ConversationMessage[] {
  if (typeof item === 'string') {
    const message = createMessage('user', item, index)
    return message ? [message] : []
  }
  if (!isRecord(item)) return []
  const type = typeof item.type === 'string' ? item.type : ''
  if (type === 'function_call' || type === 'tool_call') {
    const name = typeof item.name === 'string' ? item.name : '未命名工具'
    const args = typeof item.arguments === 'string' ? item.arguments : JSON.stringify(item.arguments ?? {}, null, 2)
    const message = createMessage('assistant', `调用工具 **${name}**\n\n\`\`\`json\n${args}\n\`\`\``, index, 'AI · 工具调用')
    return message ? [message] : []
  }
  if (type === 'function_call_output' || type === 'tool_result') {
    const message = createMessage('tool', textContent(item.output ?? item.content), index)
    return message ? [message] : []
  }
  const role = asRole(item.role)
  const content = textContent(item.content ?? item.text)
  const messages: ConversationMessage[] = []
  const message = createMessage(role, content, index)
  if (message) messages.push(message)
  const toolCalls = toolCallContent(item.tool_calls)
  const toolMessage = createMessage('assistant', toolCalls, index + 10000, 'AI · 工具调用')
  if (toolMessage) messages.push(toolMessage)
  return messages
}

export function requestConversation(value: string): ConversationMessage[] {
  const { payload } = parseStoredPayload(value)
  if (!payload) return []
  const messages: ConversationMessage[] = []
  const instructions = textContent(payload.instructions)
  const instructionMessage = createMessage('developer', instructions, -1)
  if (instructionMessage) messages.push(instructionMessage)
  const input = Array.isArray(payload.messages) ? payload.messages : payload.input
  if (typeof input === 'string') {
    const message = createMessage('user', input, 0)
    if (message) messages.push(message)
  } else if (Array.isArray(input)) {
    input.forEach((item, index) => messages.push(...messageFromItem(item, index)))
  }
  return messages
}

function responseObjectMessages(payload: Record<string, unknown>): ConversationMessage[] {
  const messages: ConversationMessage[] = []
  if (payload.error !== undefined && payload.error !== null) {
    const content = isRecord(payload.error)
      ? textContent(payload.error.message ?? payload.error) || formattedJSON(payload.error)
      : textContent(payload.error)
    const message = createMessage('error', content, 0)
    if (message) messages.push(message)
  }
  if (Array.isArray(payload.choices)) {
    payload.choices.forEach((choice, index) => {
      if (!isRecord(choice)) return
      const item = isRecord(choice.message) ? choice.message : choice.delta
      messages.push(...messageFromItem(item, index))
    })
  }
  if (Array.isArray(payload.output)) {
    payload.output.forEach((item, index) => messages.push(...messageFromItem(item, index)))
  }
  if (messages.length === 0) {
    const outputText = textContent(payload.output_text)
    const message = createMessage('assistant', outputText, 0)
    if (message) messages.push(message)
  }
  return messages
}

function sseResponseMessages(value: string): ConversationMessage[] {
  let assistant = ''
  let reasoning = ''
  let completed: Record<string, unknown> | null = null
  const errors: ConversationMessage[] = []
  const toolCalls = new Map<string, { name: string; arguments: string }>()
  for (const rawLine of value.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line.startsWith('data:')) continue
    const data = line.slice(5).trim()
    if (!data || data === '[DONE]') continue
    let event: unknown
    try {
      event = JSON.parse(data)
    } catch {
      continue
    }
    if (!isRecord(event)) continue
    const type = typeof event.type === 'string' ? event.type : ''
    if (type === 'response.output_text.delta' && typeof event.delta === 'string') assistant += event.delta
    if (type === 'response.reasoning_summary_text.delta' && typeof event.delta === 'string') reasoning += event.delta
    if (type === 'response.function_call_arguments.delta' && typeof event.delta === 'string') {
      const id = typeof event.item_id === 'string' ? event.item_id : 'tool'
      const current = toolCalls.get(id) ?? { name: typeof event.name === 'string' ? event.name : '未命名工具', arguments: '' }
      current.arguments += event.delta
      toolCalls.set(id, current)
    }
    if (isRecord(event.response) && (type === 'response.completed' || type === 'response.failed')) completed = event.response
    if (type === 'error' || type === 'response.failed') {
      const content = textContent(event.error) || textContent(isRecord(event.response) ? event.response.error : '')
      const message = createMessage('error', content, errors.length)
      if (message) errors.push(message)
    }
    if (Array.isArray(event.choices)) {
      for (const choice of event.choices) {
        if (!isRecord(choice) || !isRecord(choice.delta)) continue
        assistant += textContent(choice.delta.content)
        const calls = choice.delta.tool_calls
        if (!Array.isArray(calls)) continue
        calls.forEach((call, index) => {
          if (!isRecord(call)) return
          const fn = isRecord(call.function) ? call.function : {}
          const id = typeof call.id === 'string' ? call.id : `chat-tool-${index}`
          const current = toolCalls.get(id) ?? { name: '', arguments: '' }
          if (typeof fn.name === 'string') current.name += fn.name
          if (typeof fn.arguments === 'string') current.arguments += fn.arguments
          toolCalls.set(id, current)
        })
      }
    }
  }

  const messages: ConversationMessage[] = []
  const reasoningMessage = createMessage('assistant', reasoning, 0, 'AI · 推理摘要')
  if (reasoningMessage) messages.push(reasoningMessage)
  const assistantMessage = createMessage('assistant', assistant, 1)
  if (assistantMessage) messages.push(assistantMessage)
  let toolIndex = 0
  for (const tool of toolCalls.values()) {
    const content = `调用工具 **${tool.name || '未命名工具'}**\n\n\`\`\`json\n${tool.arguments}\n\`\`\``
    const message = createMessage('assistant', content, 100 + toolIndex, 'AI · 工具调用')
    if (message) messages.push(message)
    toolIndex += 1
  }
  if (messages.length === 0 && completed) messages.push(...responseObjectMessages(completed))
  messages.push(...errors)
  return messages
}

export function responseConversation(value: string): ConversationMessage[] {
  if (!value.trim()) return []
  if (/^\s*(?:event:|data:)/m.test(value)) return sseResponseMessages(value)
  const { payload } = parseStoredPayload(value)
  return payload ? responseObjectMessages(payload) : []
}

export function conversation(value: string, response: string): ConversationMessage[] {
  return [...requestConversation(value), ...responseConversation(response)]
}

export function deltaDescription(value: string): string {
  const { delta } = parseStoredPayload(value)
  if (!delta) return ''
  const itemCount = Object.values(delta.omittedItems ?? {}).reduce((sum, count) => sum + count, 0)
  const fieldCount = delta.omittedFields?.length ?? 0
  const details: string[] = []
  if (itemCount > 0) details.push(`${itemCount} 条已记录消息`)
  if (fieldCount > 0) details.push(`${fieldCount} 个未变化字段`)
  const base = delta.mode === 'attempt' ? '与原始请求相同的内容' : '上一请求已保存的上下文'
  return details.length > 0 ? `增量日志已省略${details.join('、')}（${base}）` : `当前仅保存相对${base}的变化`
}
