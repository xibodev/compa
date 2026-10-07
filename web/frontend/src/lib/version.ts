/**
 * Whether a build version means something to a person: a release number
 * such as "1.4.0" or "v0.9.2-rc.1", not a development build's "dev".
 */
export function isReleaseVersion(version: string | undefined): boolean {
  const value = version?.trim() ?? ""
  return /^v?\d+\.\d+(\.\d+)?([-+][0-9A-Za-z.-]+)?$/.test(value)
}
