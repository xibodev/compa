import type { Components, Options } from "react-markdown"
import rehypeRaw from "rehype-raw"
import rehypeSanitize from "rehype-sanitize"
import remarkGfm from "remark-gfm"

import {
  MarkdownImage,
  MarkdownLink,
} from "@/components/chat/markdown-components"
import { MarkdownCodeBlock } from "@/components/chat/message-code-block"

/** How replies and skills render their markdown, everywhere it is shown. */
export const MARKDOWN_REMARK_PLUGINS: NonNullable<Options["remarkPlugins"]> = [
  remarkGfm,
]
// Raw HTML is parsed, then sanitized. Code blocks highlight themselves.
export const MARKDOWN_REHYPE_PLUGINS: NonNullable<Options["rehypePlugins"]> = [
  rehypeRaw,
  rehypeSanitize,
]

export const MARKDOWN_COMPONENTS: Components = {
  pre: MarkdownCodeBlock,
  a: MarkdownLink,
  img: MarkdownImage,
}
