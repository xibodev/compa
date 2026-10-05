import { isLauncherAuthPathname } from "@/lib/launcher-login-path"

/** A response that was not ok, keeping its status for callers that act on it. */
export class HttpError extends Error {
  readonly status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = "HttpError"
    this.status = status
  }
}

/** True for a 4xx answer: asking again will not change it. */
export function isClientError(error: unknown): boolean {
  return error instanceof HttpError && error.status >= 400 && error.status < 500
}

/**
 * Same-origin fetch that sends cookies; redirects to launcher login on 401 JSON responses.
 * Skips redirect while already on an auth page (login or setup) to avoid reload loops.
 */
export async function launcherFetch(
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  const res = await fetch(input, {
    credentials: "same-origin",
    ...init,
  })
  if (res.status === 401) {
    const ct = res.headers.get("content-type") || ""
    if (
      ct.includes("application/json") &&
      typeof globalThis.location !== "undefined" &&
      !isLauncherAuthPathname(globalThis.location.pathname)
    ) {
      globalThis.location.assign("/launcher-login")
    }
  }
  return res
}
