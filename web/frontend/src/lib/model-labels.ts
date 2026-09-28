import type { ProviderTarget } from "@/api/provider-instances"

/** How a model reads to a person, with the exact target kept for a tooltip. */
export interface ModelLabel {
  /** The model's name, e.g. its display name. */
  model: string
  /** The provider serving it; empty when it is not known. */
  provider: string
  /** The exact selection it stands for, e.g. "instance-id/model-id". */
  target: string
}

/** The model's display name, falling back to the catalog's model id. */
export function targetModelLabel(target: ProviderTarget): string {
  return (
    target.label?.trim() || target.display_name?.trim() || target.model_id
  )
}

/** The display name of the instance serving the model, else its id. */
export function targetInstanceLabel(target: ProviderTarget): string {
  return target.instance_label?.trim() || target.instance_id
}

/**
 * The label of a selection: a known exact target reads as its model and
 * provider names; an unknown "instance/model" target reads as its model id
 * and instance id; anything else, such as a route name, reads as itself.
 */
export function selectionLabel(
  selection: string,
  targets: readonly ProviderTarget[],
): ModelLabel {
  const known = targets.find((target) => target.target === selection)
  if (known) {
    return {
      model: targetModelLabel(known),
      provider: targetInstanceLabel(known),
      target: selection,
    }
  }
  const slash = selection.indexOf("/")
  if (slash > 0 && slash < selection.length - 1) {
    return {
      model: selection.slice(slash + 1),
      provider: selection.slice(0, slash),
      target: selection,
    }
  }
  return { model: selection, provider: "", target: selection }
}

/** "model · provider", or just the model when the provider is unknown. */
export function formatModelLabel(label: ModelLabel): string {
  return label.provider ? `${label.model} · ${label.provider}` : label.model
}
