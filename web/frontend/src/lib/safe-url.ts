/**
 * Returns url when it is an http(s) address, so a link that came from a
 * registry, a module or the extension can never run script.
 */
export function safeExternalURL(url: string | undefined): string | undefined {
  if (!url) return undefined
  try {
    const parsed = new URL(url)
    return parsed.protocol === "https:" || parsed.protocol === "http:"
      ? parsed.href
      : undefined
  } catch {
    return undefined
  }
}

/** True for an http(s) address on another origin than the dashboard's. */
export function isRemoteURL(url: string): boolean {
  if (typeof window === "undefined") return false
  try {
    const parsed = new URL(url, window.location.href)
    return (
      (parsed.protocol === "https:" || parsed.protocol === "http:") &&
      parsed.origin !== window.location.origin
    )
  } catch {
    return false
  }
}
