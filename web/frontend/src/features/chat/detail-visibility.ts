import { getSafeLocalStorage } from "@/lib/storage"

export type AssistantDetailVisibility =
  "none" | "thought" | "tool_calls" | "all"

export type AssistantDetailMessageKind =
  "normal" | "thought" | "tool_calls" | undefined

export const ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY =
  "compa:chat-assistant-detail-visibility"
export const DEFAULT_ASSISTANT_DETAIL_VISIBILITY: AssistantDetailVisibility =
  "all"

function isAssistantDetailVisibility(
  value: unknown,
): value is AssistantDetailVisibility {
  return (
    value === "none" ||
    value === "thought" ||
    value === "tool_calls" ||
    value === "all"
  )
}

function parseAssistantDetailVisibility(
  rawValue: string | null,
): AssistantDetailVisibility | undefined {
  if (rawValue === null) {
    return undefined
  }

  try {
    const value: unknown = JSON.parse(rawValue)
    return isAssistantDetailVisibility(value) ? value : undefined
  } catch {
    return undefined
  }
}

// Storage failures (privacy mode, quota) keep the in-memory atom state.
export const assistantDetailVisibilityStorage = {
  getItem(
    key: string,
    initialValue: AssistantDetailVisibility,
  ): AssistantDetailVisibility {
    try {
      return (
        parseAssistantDetailVisibility(
          getSafeLocalStorage()?.getItem(key) ?? null,
        ) ?? initialValue
      )
    } catch {
      return initialValue
    }
  },
  setItem(key: string, newValue: AssistantDetailVisibility) {
    try {
      getSafeLocalStorage()?.setItem(key, JSON.stringify(newValue))
    } catch {
      // Keep the in-memory atom state.
    }
  },
  removeItem(key: string) {
    try {
      getSafeLocalStorage()?.removeItem(key)
    } catch {
      // Keep the in-memory atom state.
    }
  },
  subscribe(
    key: string,
    callback: (value: AssistantDetailVisibility) => void,
    initialValue: AssistantDetailVisibility,
  ) {
    if (
      typeof window === "undefined" ||
      typeof window.addEventListener !== "function"
    ) {
      return undefined
    }

    const handleStorage = (event: StorageEvent) => {
      if (event.key !== key || event.storageArea !== getSafeLocalStorage()) {
        return
      }

      callback(parseAssistantDetailVisibility(event.newValue) ?? initialValue)
    }

    window.addEventListener("storage", handleStorage)
    return () => window.removeEventListener("storage", handleStorage)
  },
}

export function shouldShowAssistantMessage(
  visibility: AssistantDetailVisibility,
  kind: AssistantDetailMessageKind,
): boolean {
  if (kind !== "thought" && kind !== "tool_calls") {
    return true
  }

  if (visibility === "all") {
    return true
  }

  if (visibility === "none") {
    return false
  }

  return visibility === kind
}
