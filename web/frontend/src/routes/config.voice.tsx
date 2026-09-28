import { createFileRoute } from "@tanstack/react-router"

import { VoicePage } from "@/components/config/voice-page"

export const Route = createFileRoute("/config/voice")({
  component: VoicePage,
})
