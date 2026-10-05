import { Fragment, memo, useMemo } from "react"

import { parseAnsiSegments } from "@/lib/ansi-log"

type AnsiLogLineProps = {
  line: string
}

export const AnsiLogLine = memo(function AnsiLogLine({
  line,
}: AnsiLogLineProps) {
  const segments = useMemo(() => {
    return parseAnsiSegments(line)
  }, [line])

  return (
    <div className="whitespace-pre">
      {segments.map((segment, index) => (
        <Fragment key={`${index}-${segment.text.length}`}>
          <span style={segment.style}>{segment.text}</span>
        </Fragment>
      ))}
    </div>
  )
})
