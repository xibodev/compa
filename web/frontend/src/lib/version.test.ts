import { describe, expect, it } from "vitest"

import { isReleaseVersion } from "./version"

describe("isReleaseVersion", () => {
  it("shows release numbers", () => {
    for (const version of ["1.4.0", "v0.9.2", "2.0", "1.0.0-rc.1", "v3.1.4+build.7"])
      expect(isReleaseVersion(version)).toBe(true)
  })

  it("hides development builds and unknown versions", () => {
    for (const version of ["dev", "", "  ", "unknown", "main", undefined])
      expect(isReleaseVersion(version)).toBe(false)
  })
})
