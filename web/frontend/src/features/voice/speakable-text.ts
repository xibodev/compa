const ARTIFACT_LINE = /^\s*@artifact\s/

/**
 * The words of a reply as they should be spoken: no @artifact lines (they are
 * file cards, not prose), no code blocks, and no markdown syntax read out as
 * symbols.
 */
export function speakableText(markdown: string): string {
  return (
    markdown
      .split("\n")
      .filter((line) => !ARTIFACT_LINE.test(line))
      .join("\n")
      // Code is for reading, not listening: a closed fence goes, and so does
      // everything after a fence that never closes.
      .replace(/^[ \t]*(`{3,}|~{3,})[^\n]*\n[\s\S]*?^[ \t]*\1[^\n]*$/gm, "")
      .replace(/^[ \t]*(`{3,}|~{3,})[^\n]*(\n[\s\S]*)?$/m, "")
      .replace(/<\/?[a-zA-Z][^>]*>/g, " ")
      // Images say their alt text, links their text.
      .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
      .replace(/`([^`\n]*)`/g, "$1")
      .replace(/^[ \t]*([-*_][ \t]*){3,}$/gm, "")
      .replace(
        /^[ \t]*\|?[ \t]*:?-{3,}:?[ \t]*(\|[ \t]*:?-{3,}:?[ \t]*)*\|?[ \t]*$/gm,
        "",
      )
      .replace(/^[ \t]{0,3}#{1,6}[ \t]+/gm, "")
      .replace(/^[ \t]{0,3}>[ \t]?/gm, "")
      .replace(/^[ \t]*([-*+]|\d+[.)])[ \t]+(\[[ xX]\][ \t]+)?/gm, "")
      .replace(/\|/g, " ")
      .replace(/(\*\*|__|~~)(.+?)\1/g, "$2")
      .replace(/(^|[^\w*])[*_]([^*_\n]+)[*_](?=[^\w*]|$)/gm, "$1$2")
      .replace(/[ \t]+/g, " ")
      .replace(/[ \t]*\n[\s]*/g, "\n")
      .trim()
  )
}
