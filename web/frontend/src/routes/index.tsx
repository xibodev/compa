import { createFileRoute } from "@tanstack/react-router"

import { ChatPage } from "@/components/chat/chat-page"
import { ChatRouteError } from "@/components/chat/chat-route-error"

export const Route = createFileRoute("/")({
  component: ChatPage,
  errorComponent: ChatRouteError,
})
