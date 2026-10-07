/**
 * Dashboard launcher auth API.
 * Uses plain fetch (not launcherFetch) to avoid redirect loops on auth pages.
 */
export type LoginResult =
  { ok: true } | { ok: false; status: number; error: string }

export async function postLauncherDashboardLogin(
  password: string,
): Promise<LoginResult> {
  const res = await fetch("/api/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ password: password.trim() }),
  })
  if (res.ok) return { ok: true }

  return {
    ok: false,
    status: res.status,
    error: await readLauncherAuthError(res),
  }
}

export type LauncherAuthStatus = {
  authenticated: boolean
  /** true when a bcrypt password has been stored in the DB */
  initialized: boolean
}

export async function getLauncherAuthStatus(): Promise<LauncherAuthStatus> {
  const res = await fetch("/api/auth/status", {
    method: "GET",
    credentials: "same-origin",
  })
  if (!res.ok) {
    throw new Error(`status ${res.status}`)
  }
  return (await res.json()) as LauncherAuthStatus
}

export async function postLauncherDashboardLogout(): Promise<boolean> {
  const res = await fetch("/api/auth/logout", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: "{}",
  })
  return res.ok
}

/** Signs every browser out, this one included. */
export async function postLauncherDashboardLogoutAll(): Promise<LoginResult> {
  const res = await fetch("/api/auth/logout-all", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: "{}",
  })
  if (res.ok) return { ok: true }
  return {
    ok: false,
    status: res.status,
    error: await readLauncherAuthError(res),
  }
}

export type SetupResult =
  { ok: true } | { ok: false; status: number; error: string }

export interface SetupOptions {
  /** The token of the setup link; the first password needs it. */
  setupToken?: string
  /** The password in use; changing it needs it. */
  currentPassword?: string
}

export async function postLauncherDashboardSetup(
  password: string,
  confirm: string,
  options: SetupOptions = {},
): Promise<SetupResult> {
  const body: Record<string, string> = {
    password: password.trim(),
    confirm: confirm.trim(),
  }
  if (options.setupToken) body.setup_token = options.setupToken
  if (options.currentPassword !== undefined) {
    body.current_password = options.currentPassword.trim()
  }
  const res = await fetch("/api/auth/setup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body),
  })
  if (res.ok) return { ok: true }
  return {
    ok: false,
    status: res.status,
    error: await readLauncherAuthError(res),
  }
}

async function readLauncherAuthError(res: Response): Promise<string> {
  let msg = `Request failed with status ${res.status}`
  try {
    const j = (await res.json()) as { error?: string }
    if (j.error) msg = j.error
  } catch {
    /* ignore */
  }
  return msg
}
