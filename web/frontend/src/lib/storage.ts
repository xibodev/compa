/**
 * localStorage that never throws. Reading the property itself throws when the
 * browser blocks storage (privacy settings, sandboxed frames), and several
 * stores read it while their module loads, so an unguarded read stops the app
 * from starting at all.
 */
export function getSafeLocalStorage(): Storage | undefined {
  try {
    return globalThis.localStorage ?? undefined
  } catch {
    return undefined
  }
}

export function readStoredValue(key: string): string | null {
  try {
    return getSafeLocalStorage()?.getItem(key) ?? null
  } catch {
    return null
  }
}

export function writeStoredValue(key: string, value: string) {
  try {
    getSafeLocalStorage()?.setItem(key, value)
  } catch {
    // Quota or privacy mode: the value stays in memory only.
  }
}

export function removeStoredValue(key: string) {
  try {
    getSafeLocalStorage()?.removeItem(key)
  } catch {
    // Nothing to remove when storage is unavailable.
  }
}
