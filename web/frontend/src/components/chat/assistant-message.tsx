import {
  IconBrain,
  IconCheck,
  IconChevronDown,
  IconCopy,
  IconDownload,
  IconFileText,
  IconTool,
} from "@tabler/icons-react"
import { memo, useState } from "react"
import { useTranslation } from "react-i18next"
import ReactMarkdown from "react-markdown"

import { BRAND_NAME } from "@/components/brand-logo"
import { ArtifactCard } from "@/components/chat/artifact-card"
import { extractArtifacts } from "@/components/chat/artifact-lines"
import {
  MARKDOWN_COMPONENTS,
  MARKDOWN_REHYPE_PLUGINS,
  MARKDOWN_REMARK_PLUGINS,
} from "@/components/chat/markdown"
import { MessageCodeBlock } from "@/components/chat/message-code-block"
import { RevealedImagesContext } from "@/components/chat/revealed-images"
import { Button } from "@/components/ui/button"
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"
import { formatMessageTime } from "@/hooks/use-web-chat"
import { cn } from "@/lib/utils"
import {
  type AssistantMessageKind,
  type ChatAttachment,
  type ChatToolCall,
} from "@/store/chat"

interface AssistantMessageProps {
  content: string
  attachments?: ChatAttachment[]
  kind?: AssistantMessageKind
  /** The name of the model that answered, as a person reads it. */
  modelName?: string
  /** The exact target that answered, shown as the model name's tooltip. */
  modelTarget?: string
  toolCalls?: ChatToolCall[]
  timestamp?: string | number
}

// Stable defaults, so a message without attachments or tool calls does not
// get new props on every render.
const EMPTY_ATTACHMENTS: ChatAttachment[] = []
const EMPTY_TOOL_CALLS: ChatToolCall[] = []

// Markdown prose. The code blocks carry their own chrome (not-prose), and
// inline code is styled once in index.css.
const PROSE =
  "prose dark:prose-invert prose-pre:my-2 prose-pre:overflow-x-auto prose-pre:rounded-lg prose-pre:border prose-pre:bg-zinc-100 prose-pre:p-0 prose-pre:text-zinc-900 dark:prose-pre:bg-zinc-950 dark:prose-pre:text-zinc-100 max-w-none [overflow-wrap:anywhere] break-words"

// Memoized: while a reply streams, only the message that changes re-renders
// and re-parses its markdown.
export const AssistantMessage = memo(function AssistantMessage({
  content,
  attachments = EMPTY_ATTACHMENTS,
  kind = "normal",
  modelName,
  modelTarget,
  toolCalls = EMPTY_TOOL_CALLS,
  timestamp = "",
}: AssistantMessageProps) {
  const { t } = useTranslation()
  const { copy, isCopied } = useCopyToClipboard()
  const isThought = kind === "thought"
  const isToolCalls = kind === "tool_calls"
  const isCollapsedBlock = isThought || isToolCalls
  // Artefacts arrive as marked lines inside the assistant text. Splitting
  // them out here means the prose reads cleanly and the files render as cards,
  // from exactly the same source the model was given.
  const { text: displayContent, artifacts } = extractArtifacts(content)
  const hasText = displayContent.trim().length > 0
  const hasToolCalls = toolCalls.length > 0
  const imageAttachments = attachments.filter(
    (attachment) => attachment.type === "image",
  )
  const fileAttachments = attachments.filter(
    (attachment) => attachment.type !== "image",
  )
  const [isExpanded, setIsExpanded] = useState(false)
  // The remote images loaded in this message stay loaded while it streams.
  const [revealedImages] = useState(() => new Set<string>())
  const formattedTimestamp =
    timestamp !== "" ? formatMessageTime(timestamp) : ""
  const toolNames = toolCalls.map((t) => t.function?.name).filter(Boolean)
  const collapsedLabel = isThought
    ? t("chat.reasoningLabel")
    : toolNames.length > 0
      ? `${t("chat.toolCallsLabel")}: ${toolNames.join(", ")}`
      : t("chat.toolCallsLabel")
  const copyMessageLabel = isCopied
    ? t("chat.copiedLabel")
    : t("chat.copyMessage")
  const trimmedModelName = modelName?.trim() ?? ""
  const modelTitle = modelTarget?.trim() || undefined

  return (
    <div className="group flex w-full min-w-0 flex-col gap-1.5">
      {!isCollapsedBlock && (
        <div className="text-muted-foreground flex min-h-7 items-center justify-between gap-2 px-1 text-xs">
          <div className="flex min-w-0 items-center gap-2">
            <span className="shrink-0">{BRAND_NAME}</span>
            {trimmedModelName && (
              <>
                <span aria-hidden="true" className="opacity-50">
                  •
                </span>
                <span className="min-w-0 truncate" title={modelTitle}>
                  {trimmedModelName}
                </span>
              </>
            )}
            {formattedTimestamp && (
              <>
                <span aria-hidden="true" className="opacity-50">
                  •
                </span>
                <span className="shrink-0 whitespace-nowrap">
                  {formattedTimestamp}
                </span>
              </>
            )}
          </div>
          {hasText && (
            <Button
              variant="ghost"
              size="icon"
              className="size-7 shrink-0 opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100 pointer-coarse:opacity-100"
              onClick={() => void copy(content)}
              aria-label={copyMessageLabel}
              title={copyMessageLabel}
            >
              {isCopied ? (
                <IconCheck className="h-4 w-4 text-green-500" />
              ) : (
                <IconCopy className="text-muted-foreground h-4 w-4" />
              )}
            </Button>
          )}
        </div>
      )}

      {(hasText || isCollapsedBlock || hasToolCalls) && (
        <div
          className={cn(
            "relative overflow-hidden rounded-xl border",
            isCollapsedBlock
              ? "border-border/30 bg-muted/20 text-muted-foreground dark:border-border/20 dark:bg-muted/10"
              : "bg-card text-card-foreground border-border/60",
          )}
        >
          {isCollapsedBlock && (
            <button
              type="button"
              aria-expanded={isExpanded}
              className="text-muted-foreground hover:text-foreground/80 flex w-full cursor-pointer items-center justify-between gap-2 px-3 py-2 text-left text-[12px] font-medium transition-colors select-none"
              onClick={() => setIsExpanded(!isExpanded)}
            >
              <span className="flex min-w-0 items-center gap-1.5">
                {isThought ? (
                  <IconBrain className="size-3.5 shrink-0" />
                ) : (
                  <IconTool className="size-3.5 shrink-0" />
                )}
                <span className="min-w-0 truncate">{collapsedLabel}</span>
                {trimmedModelName && (
                  <span
                    className="text-muted-foreground/70 hidden min-w-0 truncate sm:inline"
                    title={modelTitle}
                  >
                    {trimmedModelName}
                  </span>
                )}
              </span>
              <span className="flex shrink-0 items-center gap-2">
                {formattedTimestamp && (
                  <span className="whitespace-nowrap opacity-70">
                    {formattedTimestamp}
                  </span>
                )}
                <IconChevronDown
                  className={cn(
                    "size-3.5 opacity-60 transition-all duration-200 group-hover:opacity-100",
                    isExpanded ? "rotate-180" : "",
                  )}
                />
              </span>
            </button>
          )}
          {(!isCollapsedBlock || isExpanded) && isToolCalls && hasToolCalls && (
            <div className="space-y-3 px-3 pt-0 pb-3">
              {toolCalls.map((toolCall, index) => {
                const explanation =
                  toolCall.extraContent?.toolFeedbackExplanation?.trim() ?? ""
                const toolName = toolCall.function?.name?.trim() ?? ""
                const toolArguments = toolCall.function?.arguments?.trim() ?? ""
                const hasFunctionSummary = toolName || toolArguments

                if (!explanation && !hasFunctionSummary) {
                  return null
                }

                return (
                  <div
                    key={toolCall.id ?? `${toolName}-${index}`}
                    className={cn(
                      "space-y-3",
                      index > 0 && "border-border/20 border-t pt-3",
                    )}
                  >
                    {explanation && (
                      <div className="space-y-1.5">
                        <div className="text-muted-foreground/55 text-[11px] font-medium tracking-wide uppercase">
                          {t("chat.toolCallExplanationLabel")}
                        </div>
                        <div className="prose dark:prose-invert prose-p:my-1.5 prose-p:whitespace-pre-wrap max-w-none text-[13px] leading-relaxed [overflow-wrap:anywhere] break-words opacity-80">
                          <RevealedImagesContext.Provider
                            value={revealedImages}
                          >
                            <ReactMarkdown
                              remarkPlugins={MARKDOWN_REMARK_PLUGINS}
                              rehypePlugins={MARKDOWN_REHYPE_PLUGINS}
                              components={MARKDOWN_COMPONENTS}
                            >
                              {explanation}
                            </ReactMarkdown>
                          </RevealedImagesContext.Provider>
                        </div>
                      </div>
                    )}

                    {hasFunctionSummary && (
                      <div
                        className={cn(
                          "space-y-1.5",
                          explanation && "border-border/20 border-t pt-3",
                        )}
                      >
                        <div className="text-muted-foreground/55 text-[11px] font-medium tracking-wide uppercase">
                          {t("chat.toolCallFunctionLabel")}
                        </div>
                        <div className="bg-background/55 border-border/25 space-y-2 rounded-lg border px-3 py-2.5">
                          {toolName && !toolArguments && (
                            <div className="text-foreground/75 font-mono text-[12px] font-semibold">
                              {toolName}
                            </div>
                          )}
                          {toolArguments && (
                            <MessageCodeBlock
                              code={toolArguments}
                              language="json"
                              label={
                                toolName || t("chat.toolCallArgumentsLabel")
                              }
                              className="my-0 shadow-none"
                              bodyClassName="px-3 py-2 text-[12px] leading-relaxed"
                            />
                          )}
                        </div>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
          {(!isCollapsedBlock || isExpanded) && !isToolCalls && hasText && (
            <div
              className={cn(
                PROSE,
                isThought
                  ? "prose-p:my-1.5 prose-p:whitespace-pre-wrap px-3 pt-0 pb-3 text-[13px] leading-relaxed opacity-80"
                  : "prose-p:my-2 prose-p:whitespace-pre-wrap p-4 text-[15px] leading-relaxed",
              )}
            >
              <RevealedImagesContext.Provider value={revealedImages}>
                <ReactMarkdown
                  remarkPlugins={MARKDOWN_REMARK_PLUGINS}
                  rehypePlugins={MARKDOWN_REHYPE_PLUGINS}
                  components={MARKDOWN_COMPONENTS}
                >
                  {displayContent}
                </ReactMarkdown>
              </RevealedImagesContext.Provider>
            </div>
          )}

          {artifacts.length > 0 && (
            <div>
              {artifacts.map((a) => (
                <ArtifactCard key={`${a.module}:${a.id}`} artifact={a} />
              ))}
            </div>
          )}
        </div>
      )}

      {imageAttachments.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-2">
          {imageAttachments.map((attachment, index) => (
            <a
              key={`${attachment.url}-${index}`}
              href={attachment.url}
              target="_blank"
              rel="noopener noreferrer"
              className="group/img border-border/50 bg-muted/30 hover:border-border/80 relative overflow-hidden rounded-xl border shadow-sm transition-colors"
            >
              <img
                src={attachment.url}
                alt={attachment.filename || t("chat.uploadedImage")}
                className="max-h-80 max-w-[280px] object-contain transition-transform duration-300 group-hover/img:scale-[1.02]"
              />
              <div className="absolute inset-0 bg-black/0 transition-colors group-hover/img:bg-black/10 dark:group-hover/img:bg-black/20" />
            </a>
          ))}
        </div>
      )}

      {fileAttachments.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-3">
          {fileAttachments.map((attachment, index) => (
            <a
              key={`${attachment.url}-${index}`}
              href={attachment.url}
              download={attachment.filename}
              className="group/file border-border/60 bg-card flex w-fit max-w-sm min-w-[220px] items-center gap-3.5 rounded-xl border px-4 py-3 transition-all duration-300 hover:-translate-y-0.5 hover:border-violet-500/30 hover:shadow-sm dark:hover:border-violet-500/40"
            >
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg text-violet-400 ring-1 ring-violet-500/10 dark:bg-violet-500/10 dark:text-violet-400 dark:ring-violet-500/30">
                <IconFileText className="h-5 w-5" />
              </div>
              <div className="flex min-w-0 flex-1 flex-col pr-1">
                <span className="text-foreground/90 truncate text-[14px] leading-tight font-medium transition-colors group-hover/file:text-violet-600 dark:group-hover/file:text-violet-400">
                  {attachment.filename || t("chat.downloadFile")}
                </span>
                <span className="text-muted-foreground/70 mt-1 text-[12px] font-medium">
                  {attachment.filename?.split(".").pop()?.toUpperCase() ||
                    t("chat.fileLabel")}
                </span>
              </div>
              <div className="bg-muted/60 text-muted-foreground/50 dark:bg-muted/20 flex h-8 w-8 shrink-0 items-center justify-center rounded-full transition-all duration-300 group-hover/file:bg-violet-400 group-hover/file:text-white group-hover/file:shadow-sm dark:group-hover/file:bg-violet-400">
                <IconDownload className="h-4 w-4 transition-transform duration-300 group-hover/file:-translate-y-[1px]" />
              </div>
            </a>
          ))}
        </div>
      )}
    </div>
  )
})
