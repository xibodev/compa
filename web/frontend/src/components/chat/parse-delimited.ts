/** How many rows to draw. A cockpit card is a preview; Download is the whole file. */
export const MAX_TABLE_ROWS = 200

/** How much of a file to look at. A card must not stall on a huge CSV. */
const MAX_TABLE_BYTES = 200_000

/**
 * parseDelimited splits CSV or TSV into rows.
 *
 * The delimiter is chosen per FILE rather than per line: a comma inside a
 * quoted field would otherwise make one line look tab-delimited and the next
 * comma-delimited, silently splitting the same column two different ways.
 *
 * Quoted fields are honoured, including embedded delimiters, newlines and
 * doubled quotes ("" for a literal quote), because a CSV that contains prose
 * almost always has them and splitting naively corrupts every row after the
 * first one that does.
 *
 * Returns an empty array when the content does not look like a table, so the
 * caller can show the raw text instead. A module that reports something
 * unexpected should LOOK wrong rather than look like nothing.
 */
export function parseDelimited(source: string): string[][] {
  const text = source.slice(0, MAX_TABLE_BYTES)
  if (!text.trim()) return []

  const delim = pickDelimiter(text)
  if (!delim) return []

  const rows: string[][] = []
  let row: string[] = []
  let field = ""
  let quoted = false

  const endRow = () => {
    row.push(field)
    field = ""
    // A trailing CR from a CRLF file belongs to the line ending, not the data.
    rows.push(row.map(stripCR))
    row = []
  }

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]

    if (quoted) {
      if (ch !== '"') {
        field += ch
      } else if (text[i + 1] === '"') {
        field += '"'
        i++
      } else {
        quoted = false
      }
      continue
    }

    if (ch === '"') {
      quoted = true
    } else if (ch === delim) {
      row.push(field)
      field = ""
    } else if (ch === "\n") {
      endRow()
      if (rows.length >= MAX_TABLE_ROWS) return rows
    } else {
      field += ch
    }
  }

  if (field !== "" || row.length > 0) endRow()

  // A single row is not a table -- it is one line of text, and drawing it as a
  // header with no body would be a worse rendering than the raw text.
  return rows.length > 1 ? rows : []
}

/**
 * pickDelimiter counts candidates OUTSIDE quotes, so a comma inside a quoted
 * field cannot outvote the real delimiter.
 */
function pickDelimiter(text: string): string | null {
  let commas = 0
  let tabs = 0
  let quoted = false

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (ch === '"') {
      if (quoted && text[i + 1] === '"') i++
      else quoted = !quoted
      continue
    }
    if (quoted) continue
    if (ch === ",") commas++
    else if (ch === "\t") tabs++
  }

  if (commas === 0 && tabs === 0) return null
  return tabs > commas ? "\t" : ","
}

function stripCR(cell: string): string {
  return cell.endsWith("\r") ? cell.slice(0, -1) : cell
}
