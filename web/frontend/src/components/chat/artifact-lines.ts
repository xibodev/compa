/**
 * The @artifact lines a reply carries. A module reports each produced file as
 * one marked JSON line; the model repeats the line in its reply, and the card
 * is drawn from it (see artifact-card.tsx).
 */

const ARTIFACT_MARKER = "@artifact "

export interface Artifact {
  id: string
  kind: string
  path: string
  root: string
  media_type: string
  /** The primitive the module asked for. Advisory only: the host resolves it
   *  and sends its decision as `primitive`. Kept for provenance. */
  presentation?: string
  /** How the HOST decided this renders, resolved through internal/view. This
   *  is the field the cockpit draws from. */
  primitive?: string
  bytes: number
  digest: string
  title?: string
  module: string
}

const isString = (value: unknown): value is string => typeof value === "string"

const isOptionalString = (value: unknown) =>
  value === undefined || value === null || typeof value === "string"

/**
 * Whether a parsed line has the shape a card is drawn from. The line is model
 * output, so valid JSON can still be anything: null, or an object where a
 * title belongs, which would break rendering of the whole chat.
 */
export function isArtifact(value: unknown): value is Artifact {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false
  }
  const a = value as Record<string, unknown>
  return (
    isString(a.id) &&
    isString(a.kind) &&
    isString(a.path) &&
    isString(a.root) &&
    isString(a.media_type) &&
    isString(a.digest) &&
    isString(a.module) &&
    typeof a.bytes === "number" &&
    Number.isFinite(a.bytes) &&
    isOptionalString(a.title) &&
    isOptionalString(a.presentation) &&
    isOptionalString(a.primitive)
  )
}

/** Extracts artefact lines from assistant text, returning the cleaned text. */
export function extractArtifacts(content: string): {
  text: string
  artifacts: Artifact[]
} {
  if (!content.includes(ARTIFACT_MARKER)) {
    return { text: content, artifacts: [] }
  }

  const artifacts: Artifact[] = []
  const kept: string[] = []

  for (const line of content.split("\n")) {
    const trimmed = line.trim()
    if (trimmed.startsWith(ARTIFACT_MARKER)) {
      try {
        const parsed: unknown = JSON.parse(
          trimmed.slice(ARTIFACT_MARKER.length),
        )
        if (isArtifact(parsed)) {
          artifacts.push(parsed)
          continue
        }
      } catch {
        // Not JSON; kept as text below.
      }
      // A malformed line stays in the text rather than vanishing: showing
      // something odd beats silently dropping what a module reported.
    }
    kept.push(line)
  }

  return { text: kept.join("\n").trim(), artifacts }
}
