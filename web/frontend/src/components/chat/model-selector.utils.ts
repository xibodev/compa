import type { ModelRoute, ProviderTarget } from "@/api/provider-instances"

export function chatSelectionValues(
  targets: ProviderTarget[],
  routes: ModelRoute[],
) {
  return {
    routes: routes.map((route) => route.name),
    targets: targets.map((target) => target.target),
  }
}
