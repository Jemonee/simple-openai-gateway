import { beginRequestActivity, endRequestActivity } from '@/composables/useRequestActivity'

interface ApiEnvelope<T> {
  /** Whether the backend operation completed successfully. */
  success: boolean
  /** Application status code mirrored from the HTTP response. */
  code: number
  /** Operation payload, or null for commands without a response body. */
  data: T | null
  /** Human-readable result or error message. */
  message: string
}

export class ApiError extends Error {
  /** HTTP status returned by the backend. */
  readonly status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  beginRequestActivity()
  try {
    let response: Response
    try {
      response = await fetch(`/api${path}`, {
        credentials: 'same-origin',
        ...options,
        headers: {
          'Content-Type': 'application/json',
          ...(options.headers ?? {}),
        },
      })
    } catch {
      throw new ApiError('无法连接到网关服务，请确认后端已经启动', 0)
    }

    const responseText = await response.text()
    let payload: ApiEnvelope<T> | null = null
    if (responseText.trim()) {
      try {
        payload = JSON.parse(responseText) as ApiEnvelope<T>
      } catch {
        throw new ApiError(`后端响应格式错误（HTTP ${response.status}）`, response.status)
      }
    }

    if (!response.ok || !payload?.success) {
      if (response.status === 401 && typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent('gateway:unauthorized'))
      }
      throw new ApiError(payload?.message || `请求失败（HTTP ${response.status}）`, response.status)
    }
    return payload.data as T
  } finally {
    endRequestActivity()
  }
}
