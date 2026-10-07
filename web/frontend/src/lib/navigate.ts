/**
 * Loads a page of the app from the server, as a sign-in change needs: the
 * new session only applies to a fresh page.
 */
export function navigateTo(path: string) {
  globalThis.location.assign(path)
}
