import { HttpError, launcherFetch } from "@/api/http"

export interface SessionSummary {
  id: string
  title: string
  preview: string
  message_count: number
  created: string
  updated: string
}

export interface SessionDetail {
  id: string
  messages: {
    role: "user" | "assistant"
    content: string
    created_at?: string
    kind?: "normal" | "thought" | "tool_calls"
    model_name?: string
    requested_selection?: string
    served_target?: string
    served_identity?: string
    media?: string[]
    attachments?: {
      type?: "image" | "audio" | "video" | "file"
      url: string
      filename?: string
      content_type?: string
    }[]
    tool_calls?: {
      id?: string
      type?: string
      function?: {
        name?: string
        arguments?: string
      }
      extra_content?: {
        tool_feedback_explanation?: string
      }
    }[]
  }[]
  summary: string
  created: string
  updated: string
}

export async function getSessions(
  offset: number = 0,
  limit: number = 20,
): Promise<SessionSummary[]> {
  const params = new URLSearchParams({
    offset: offset.toString(),
    limit: limit.toString(),
  })

  const res = await launcherFetch(`/api/sessions?${params.toString()}`)
  if (!res.ok) {
    throw new HttpError(`Failed to fetch sessions: ${res.status}`, res.status)
  }
  return res.json()
}

/** The server holds no session with the id asked for, e.g. it was deleted. */
export class SessionNotFoundError extends Error {
  constructor(id: string) {
    super(`Session ${id} was not found`)
    this.name = "SessionNotFoundError"
  }
}

export async function getSessionHistory(id: string): Promise<SessionDetail> {
  const res = await launcherFetch(`/api/sessions/${encodeURIComponent(id)}`)
  if (res.status === 404) {
    throw new SessionNotFoundError(id)
  }
  if (!res.ok) {
    throw new HttpError(
      `Failed to fetch session ${id}: ${res.status}`,
      res.status,
    )
  }
  return res.json()
}

export async function deleteSession(id: string): Promise<void> {
  const res = await launcherFetch(`/api/sessions/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
  if (!res.ok) {
    throw new HttpError(
      `Failed to delete session ${id}: ${res.status}`,
      res.status,
    )
  }
}
