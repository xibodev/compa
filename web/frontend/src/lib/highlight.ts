import hljs from "highlight.js/lib/core"
import bash from "highlight.js/lib/languages/bash"
import css from "highlight.js/lib/languages/css"
import diff from "highlight.js/lib/languages/diff"
import go from "highlight.js/lib/languages/go"
import javascript from "highlight.js/lib/languages/javascript"
import json from "highlight.js/lib/languages/json"
import markdown from "highlight.js/lib/languages/markdown"
import python from "highlight.js/lib/languages/python"
import shell from "highlight.js/lib/languages/shell"
import sql from "highlight.js/lib/languages/sql"
import typescript from "highlight.js/lib/languages/typescript"
import xml from "highlight.js/lib/languages/xml"
import yaml from "highlight.js/lib/languages/yaml"

// The one highlighter for every code block, with the languages chat replies
// and skills mostly use. Anything else renders as plain text.
hljs.registerLanguage("bash", bash)
hljs.registerLanguage("css", css)
hljs.registerLanguage("diff", diff)
hljs.registerLanguage("go", go)
hljs.registerLanguage("javascript", javascript)
hljs.registerLanguage("json", json)
hljs.registerLanguage("markdown", markdown)
hljs.registerLanguage("python", python)
hljs.registerLanguage("shell", shell)
hljs.registerLanguage("sql", sql)
hljs.registerLanguage("typescript", typescript)
hljs.registerLanguage("xml", xml)
hljs.registerLanguage("yaml", yaml)

/** Highlighted HTML for code, or null when the language is unknown. */
export function highlightCode(
  code: string,
  language?: string | null,
): string | null {
  if (!language || !hljs.getLanguage(language)) {
    return null
  }

  try {
    return hljs.highlight(code, { language, ignoreIllegals: true }).value
  } catch {
    return null
  }
}
