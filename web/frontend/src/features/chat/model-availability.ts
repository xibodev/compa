import type { ModelRoute, ProviderTarget } from "@/api/provider-instances"

/**
 * What Chat can send with:
 * - loading: the default model or the selectable models are still loading
 * - loadFailed: loading failed and no model is known to be usable
 * - unavailable: there is no default model and nothing to select
 * - invalid: this chat's own selection is no longer selectable
 * - unselected: there is no default model and this chat picked none,
 *   though models can be selected
 * - ready: the chat sends with its own selection or the default model
 */
export type ChatModelAvailability =
  "loading" | "loadFailed" | "unavailable" | "invalid" | "unselected" | "ready"

export interface ChatModelSources {
  loading: boolean
  /** The configured default selection; empty when none is set. */
  defaultSelection: string
  /** The default model could not be loaded, so it is unknown. */
  defaultFailed: boolean
  /** The exact targets and routes the Chat selector offers. */
  targets: readonly ProviderTarget[]
  routes: readonly ModelRoute[]
  /** The targets or routes could not be loaded. */
  selectionsFailed: boolean
  /** This chat's own selection; empty uses the default model. */
  selection: string
}

export function resolveChatModelAvailability({
  loading,
  defaultSelection,
  defaultFailed,
  targets,
  routes,
  selectionsFailed,
  selection,
}: ChatModelSources): ChatModelAvailability {
  if (loading) return "loading"
  const hasDefault = defaultSelection !== ""
  if (!hasDefault && targets.length === 0 && routes.length === 0)
    return defaultFailed || selectionsFailed ? "loadFailed" : "unavailable"
  if (selection) {
    const selectable =
      selection === defaultSelection ||
      targets.some((target) => target.target === selection) ||
      routes.some((route) => route.name === selection)
    // A selection cannot be checked against lists that failed to load.
    return selectable || selectionsFailed ? "ready" : "invalid"
  }
  // An unknown default may well be set; the gateway resolves it.
  return hasDefault || defaultFailed ? "ready" : "unselected"
}
