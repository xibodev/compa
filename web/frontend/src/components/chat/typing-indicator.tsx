import { useTranslation } from "react-i18next"

export function TypingIndicator() {
  const { t } = useTranslation()

  return (
    <div className="flex w-full flex-col gap-1.5">
      <div className="bg-card border-border/50 inline-flex w-fit max-w-xs flex-col gap-3 rounded-xl border px-5 py-4">
        <div className="flex items-center gap-1.5">
          <span className="size-2 animate-bounce rounded-full bg-violet-400/70 [animation-delay:-0.3s] motion-reduce:animate-none" />
          <span className="size-2 animate-bounce rounded-full bg-violet-400/70 [animation-delay:-0.15s] motion-reduce:animate-none" />
          <span className="size-2 animate-bounce rounded-full bg-violet-400/70 motion-reduce:animate-none" />
        </div>

        <div className="bg-muted relative h-1 w-36 overflow-hidden rounded-full">
          <div className="absolute inset-0 animate-[shimmer_2s_infinite] rounded-full bg-gradient-to-r from-violet-500/60 via-violet-400/80 to-violet-500/60 bg-[length:200%_100%] motion-reduce:animate-none" />
        </div>

        {/* One steady status: a screen reader announces it once instead of
            a new canned phrase every few seconds. */}
        <p role="status" className="text-foreground/80 text-sm">
          <span className="inline-block animate-[fadeSlideIn_0.4s_ease-out] motion-reduce:animate-none">
            {t("chat.thinking.step1")}
          </span>
        </p>
      </div>
    </div>
  )
}
