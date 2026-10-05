import { HttpError, launcherFetch } from "@/api/http"

/**
 * The default model: the selection a chat without its own model runs with.
 * A selection is an exact target "instance-id/model-id" or a model route
 * name; an empty selection means no default is set.
 */
export interface DefaultModel {
  selection: string
}

const PATH = "/api/default-model"

async function request(init?: RequestInit): Promise<DefaultModel> {
  const response = await launcherFetch(PATH, init)
  if (!response.ok) {
    const detail = (await response.text()).trim()
    throw new HttpError(
      detail || response.statusText || `Request failed (${response.status})`,
      response.status,
    )
  }
  const body = (await response.json()) as Partial<DefaultModel>
  return {
    selection: typeof body.selection === "string" ? body.selection.trim() : "",
  }
}

export const getDefaultModel = () => request()

/**
 * Sets the default model; an empty selection clears it. The server rejects,
 * with a plain-text reason, a selection that does not resolve.
 */
export const setDefaultModel = (selection: string) =>
  request({
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ selection: selection.trim() }),
  })
