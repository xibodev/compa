import { createFileRoute } from "@tanstack/react-router"

import { ModelsWorkspace } from "@/components/models/models-workspace"

export const Route = createFileRoute("/models")({
  component: ModelsWorkspace,
})
