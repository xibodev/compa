import * as React from "react"
import { useTranslation } from "react-i18next"

import { launcherFetch } from "@/api/http"

import type { Artifact } from "./artifact-lines"
import { parseDelimited } from "./parse-delimited"

/**
 * Artefact cards.
 *
 * A module reports produced files as pointers, never inline payloads. The
 * agent receives them as marked JSON lines in its tool result; the cockpit
 * detects the same lines and renders a card.
 *
 * Both readers see identical facts on purpose. A separate structured channel
 * would have to be threaded through the message bus and could silently
 * disagree with what the model was told -- and a card describing something the
 * agent never saw is worse than no card.
 */

/**
 * Primitives the host knows how to draw, mirroring internal/view.
 *
 * A media type is a CLAIM, not a fact: a provider was observed declaring
 * image/png for JPEG bytes. Resolution degrades to a harmless renderer rather
 * than trusting the label, and anything unrecognised is offered for download
 * rather than dropped.
 */
type Primitive =
  | "video"
  | "audio"
  | "image"
  | "document"
  | "diagram"
  | "table"
  | "markdown"
  | "text"
  | "json"
  | "image_grid"
  | "timeline"
  | "slides"
  | "download"

const KNOWN: ReadonlySet<string> = new Set([
  "video",
  "audio",
  "image",
  "image_grid",
  "timeline",
  "document",
  "diagram",
  "slides",
  "table",
  "markdown",
  "text",
  "json",
  "download",
])

/**
 * The HOST decides how an artefact renders, and sends the answer as
 * `primitive`. This function only checks that the answer is one the cockpit
 * can actually draw; the media-type mapping lives on the host alone
 * (internal/view), so the two sides cannot disagree about a file.
 *
 * An unrecognised primitive degrades to `download` rather than being honoured:
 * the cockpit draws the shapes it owns, and cannot be told to invent one. A
 * response with no primitive at all also degrades to `download`, so an
 * artefact is always retrievable even when it cannot be shown inline.
 */
function resolvePrimitive(a: Artifact): Primitive {
  const decided = (a.primitive || "").trim()
  if (decided && KNOWN.has(decided)) return decided as Primitive
  return "download"
}

function artifactURL(a: Artifact): string {
  const params = new URLSearchParams({
    module: a.module,
    root: a.root,
    path: a.path,
  })
  return `/api/modules/artifact?${params.toString()}`
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

export function ArtifactCard({ artifact: a }: { artifact: Artifact }) {
  const { t } = useTranslation()
  const primitive = resolvePrimitive(a)
  const url = artifactURL(a)
  const [showText, setShowText] = React.useState(false)
  const [text, setText] = React.useState("")
  const [reachable, setReachable] = React.useState<boolean | null>(null)

  // Confirm the host will actually serve this artefact.
  //
  // A card is drawn from any @artifact line in assistant text, so a user who
  // asks the model to repeat such a line gets a card -- demonstrated, and the
  // forged card renders. It cannot READ anything the host would not already
  // serve, because /api/modules/artifact resolves every path against the roots
  // actually granted, but a card that looks identical to a real one is still a
  // claim the interface should not make on the model's word alone.
  //
  // A HEAD asks the host the only question that matters: will you serve this?
  // A card whose artefact the host refuses says so instead of looking verified.
  React.useEffect(() => {
    let cancelled = false
    launcherFetch(url, { method: "HEAD" })
      .then((res) => {
        if (!cancelled) setReachable(res.ok)
      })
      .catch(() => {
        if (!cancelled) setReachable(false)
      })
    return () => {
      cancelled = true
    }
  }, [url])

  const loadText = async () => {
    if (text) {
      setShowText(!showText)
      return
    }
    try {
      const res = await launcherFetch(url)
      // An error page is not the artefact's contents.
      if (!res.ok) throw new Error(String(res.status))
      setText(await res.text())
      setShowText(true)
    } catch (err) {
      setText(
        t("chat.artifact.readFailed", {
          error: err instanceof Error ? err.message : String(err),
        }),
      )
      setShowText(true)
    }
  }

  if (reachable === false) {
    return (
      <div className="border-destructive/60 bg-destructive/5 my-2 rounded-lg border p-3">
        <p className="text-sm font-medium">{a.title || a.id}</p>
        <p className="text-muted-foreground mt-1 text-xs">
          {t("chat.artifact.notServed")}
        </p>
      </div>
    )
  }

  return (
    <div className="border-border/60 bg-muted/20 my-2 rounded-lg border p-3">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{a.title || a.id}</p>
          <p className="text-muted-foreground truncate font-mono text-[11px]">
            {a.kind} · {formatBytes(a.bytes)} · {a.module}
          </p>
        </div>
        <a
          href={url}
          download
          className="text-muted-foreground hover:text-foreground shrink-0 text-xs underline"
        >
          {t("chat.artifact.download")}
        </a>
      </div>

      <div className="mt-2">
        {primitive === "video" && (
          <video src={url} controls className="max-h-96 w-full rounded" />
        )}
        {primitive === "audio" && (
          <audio src={url} controls className="w-full" />
        )}
        {primitive === "image" && (
          <img src={url} alt={a.title || a.id} className="max-h-96 rounded" />
        )}
        {/* SVG is a drawable image; mermaid and graphviz sources are text the
            host serves as text/plain, which <img> cannot draw, so each is
            rendered as what it is. */}
        {primitive === "diagram" &&
          (isDrawableImage(a.media_type) ? (
            <img
              src={url}
              alt={a.title || a.id}
              className="max-h-96 rounded bg-white p-2"
            />
          ) : (
            <>
              <button
                type="button"
                onClick={() => void loadText()}
                className="text-muted-foreground hover:text-foreground text-xs underline"
              >
                {showText
                  ? t("chat.artifact.hideDiagramSource")
                  : t("chat.artifact.showDiagramSource")}
              </button>
              {showText && (
                <pre className="bg-muted/40 mt-1.5 max-h-72 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
                  {text.slice(0, 8000)}
                </pre>
              )}
            </>
          ))}
        {/* A PDF opens in its own tab. The host serves artefacts under a
            sandboxing CSP, which keeps an embedded <object> from showing it;
            the office document formats have no browser viewer at all. The
            media_type here is the MODULE's claim, so only PDF gets the link. */}
        {primitive === "document" &&
          (isInlineDocument(a.media_type) ? (
            <a
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-muted-foreground hover:text-foreground text-xs underline"
            >
              {t("chat.artifact.openDocument")}
            </a>
          ) : (
            <p className="text-muted-foreground text-xs">
              {t("chat.artifact.noDocumentViewer")}
            </p>
          ))}
        {(primitive === "json" ||
          primitive === "markdown" ||
          primitive === "text") && (
          <>
            <button
              type="button"
              onClick={() => void loadText()}
              className="text-muted-foreground hover:text-foreground text-xs underline"
            >
              {showText
                ? t("chat.artifact.hideContents")
                : t("chat.artifact.showContents")}
            </button>
            {showText && (
              <pre className="bg-muted/40 mt-1.5 max-h-72 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
                {text.slice(0, 8000)}
              </pre>
            )}
          </>
        )}
        {/* A table renders as a grid rather than as raw text: that is the one
            thing the primitive exists for. */}
        {primitive === "table" && (
          <>
            <button
              type="button"
              onClick={() => void loadText()}
              className="text-muted-foreground hover:text-foreground text-xs underline"
            >
              {showText
                ? t("chat.artifact.hideTable")
                : t("chat.artifact.showTable")}
            </button>
            {showText && <DelimitedTable source={text} />}
          </>
        )}
        {primitive === "image_grid" && (
          <img src={url} alt={a.title || a.id} className="max-h-96 rounded" />
        )}
        {/* A deck is not an image. Slides resolve from .pptx and .ppt, which a
            browser cannot draw with <img>. Until the host can page a deck, say
            so and keep the file reachable. */}
        {primitive === "slides" && (
          <p className="text-muted-foreground text-xs">
            {t("chat.artifact.noSlidesViewer")}
          </p>
        )}
        {/* The toggle loads the artefact and renders its time ranges. */}
        {primitive === "timeline" && (
          <>
            <button
              type="button"
              onClick={() => void loadText()}
              className="text-muted-foreground hover:text-foreground text-xs underline"
            >
              {showText
                ? t("chat.artifact.hideTimeRanges")
                : t("chat.artifact.showTimeRanges")}
            </button>
            {showText && <Timeline source={text} />}
          </>
        )}
        {primitive === "download" && (
          <p className="text-muted-foreground text-xs">
            {t("chat.artifact.noViewer")}
          </p>
        )}
      </div>

      {/* The digest is shown so a person can check it -- and it is a CLAIM
          made by the line, not proof the host verified anything.
          
          A card is drawn from any @artifact line in assistant text, and a user
          who asks the model to repeat such a line gets a card. That was
          demonstrated: the forged card renders. What it cannot do is read
          anything, because /api/modules/artifact resolves every path against
          the roots the host actually granted and refuses the rest -- traversal,
          undeclared roots and read-only roots were all confirmed refused.
          
          So the exposure is presentation, not access, and the honest fix is to
          stop the card implying a verification it cannot perform. */}
      <p
        className="text-muted-foreground mt-2 truncate font-mono text-[10px]"
        title={a.digest}
      >
        {a.digest}
      </p>
    </div>
  )
}

/**
 * Timeline renders labelled time ranges on an axis, read-only.
 *
 * It exists because scene plans and edit decisions carry real start/end
 * seconds, and a table of numbers answers "is the pacing sane" badly. It
 * presents a time axis; it does not edit one -- editing is the agent's job
 * through its tools.
 *
 * The source is module output, so it is parsed defensively: anything that is
 * not a recognisable list of ranges falls back to showing the raw text rather
 * than rendering an empty axis. A module that reports something unexpected
 * should look wrong, not look like nothing.
 */
function Timeline({ source }: { source: string }) {
  const ranges = React.useMemo(() => parseRanges(source), [source])

  if (ranges.length === 0) {
    return (
      <pre className="bg-muted/40 mt-1.5 max-h-72 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
        {source.slice(0, 8000)}
      </pre>
    )
  }

  const end = Math.max(...ranges.map((r) => r.end))
  const span = end > 0 ? end : 1

  return (
    <div className="mt-2 flex flex-col gap-1">
      {ranges.map((r, i) => (
        <div key={i} className="flex items-center gap-2">
          <span className="text-muted-foreground w-28 shrink-0 truncate font-mono text-[10px]">
            {r.label || `#${i + 1}`}
          </span>
          <div className="bg-muted/40 relative h-3 flex-1 overflow-hidden rounded">
            <div
              className="bg-primary/70 absolute inset-y-0 rounded"
              style={{
                left: `${(r.start / span) * 100}%`,
                width: `${Math.max(((r.end - r.start) / span) * 100, 0.5)}%`,
              }}
            />
          </div>
          <span className="text-muted-foreground w-24 shrink-0 text-right font-mono text-[10px]">
            {r.start.toFixed(2)}–{r.end.toFixed(2)}s
          </span>
        </div>
      ))}
    </div>
  )
}

interface TimeRange {
  start: number
  end: number
  label: string
}

/**
 * Pulls time ranges out of module JSON without insisting on one shape.
 *
 * Modules name these fields differently -- in_seconds/out_seconds for cuts,
 * start/end for scenes -- and the cockpit should not force one vocabulary on
 * every module that has a timeline. Anything unparseable yields an empty list,
 * and the caller shows the raw text instead.
 */
function parseRanges(source: string): TimeRange[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(source)
  } catch {
    return []
  }

  const rows = findRangeArray(parsed)
  const out: TimeRange[] = []
  for (const row of rows) {
    if (typeof row !== "object" || row === null) continue
    const r = row as Record<string, unknown>
    const start = numberOf(r, ["in_seconds", "start_seconds", "start", "from"])
    const end = numberOf(r, ["out_seconds", "end_seconds", "end", "to"])
    if (start === null || end === null || end < start) continue
    const label = ["label", "title", "name", "id", "scene"]
      .map((k) => r[k])
      .find((v): v is string => typeof v === "string" && v.trim() !== "")
    out.push({ start, end, label: label ?? "" })
  }
  return out
}

/** Finds the first array of objects that looks like time ranges. */
function findRangeArray(value: unknown, depth = 0): unknown[] {
  if (depth > 4 || value === null || typeof value !== "object") return []
  if (Array.isArray(value)) {
    const looksLikeRanges = value.some(
      (v) =>
        typeof v === "object" &&
        v !== null &&
        numberOf(v as Record<string, unknown>, [
          "in_seconds",
          "start_seconds",
          "start",
          "from",
        ]) !== null,
    )
    return looksLikeRanges ? value : []
  }
  for (const nested of Object.values(value as Record<string, unknown>)) {
    const found = findRangeArray(nested, depth + 1)
    if (found.length > 0) return found
  }
  return []
}

function numberOf(row: Record<string, unknown>, keys: string[]): number | null {
  for (const k of keys) {
    const v = row[k]
    if (typeof v === "number" && Number.isFinite(v)) return v
  }
  return null
}

/** SVG is the one diagram format a browser can draw directly. */
function isDrawableImage(mediaType: string): boolean {
  return (
    (mediaType || "").toLowerCase().split(";")[0].trim() === "image/svg+xml"
  )
}

/** Formats a browser renders inline in an <object>. Office formats do not. */
function isInlineDocument(mediaType: string): boolean {
  const mt = (mediaType || "").toLowerCase().split(";")[0].trim()
  return mt === "application/pdf"
}

/**
 * DelimitedTable draws CSV and TSV as a grid.
 *
 * It falls back to the raw text when the content does not parse as rows, for
 * the same reason Timeline does: a module that reports something unexpected
 * should LOOK wrong rather than look like nothing. An empty grid would hide
 * both the data and the mistake.
 */
function DelimitedTable({ source }: { source: string }) {
  const rows = React.useMemo(() => parseDelimited(source), [source])

  if (rows.length === 0) {
    return (
      <pre className="bg-muted/40 mt-1.5 max-h-72 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
        {source.slice(0, 8000)}
      </pre>
    )
  }

  const [head, ...body] = rows

  return (
    /* The container scrolls, not the page: a wide table must never make the
       whole conversation scroll sideways. */
    <div className="mt-1.5 max-h-72 overflow-auto rounded border">
      <table className="w-full border-collapse text-[11px]">
        <thead className="bg-muted/60 sticky top-0">
          <tr>
            {head.map((cell, i) => (
              <th key={i} className="border-b px-2 py-1 text-left font-medium">
                {cell}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {body.map((row, r) => (
            <tr key={r} className="odd:bg-muted/20">
              {head.map((_, c) => (
                <td key={c} className="border-b px-2 py-1 align-top font-mono">
                  {row[c] ?? ""}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
