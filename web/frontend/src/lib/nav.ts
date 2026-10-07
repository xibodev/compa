/**
 * The navigation entry a pathname belongs to: the longest entry URL that is
 * the pathname or one of its parents, so /config/voice selects Voice rather
 * than Config while /config/raw still selects Config. "/" matches only
 * itself.
 */
export function activeNavUrl(
  pathname: string,
  urls: readonly string[],
): string | undefined {
  let best: string | undefined
  for (const url of urls) {
    const matches =
      url === "/"
        ? pathname === "/"
        : pathname === url || pathname.startsWith(`${url}/`)
    if (matches && (best === undefined || url.length > best.length)) best = url
  }
  return best
}
