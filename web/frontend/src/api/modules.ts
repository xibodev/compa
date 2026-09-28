import { launcherFetch } from "@/api/http"

/**
 * Detached modules.
 *
 * A module is a separate local process that contributes capabilities to the
 * agent. Installing one grants it nothing by itself: the host supplies
 * filesystem roots and subprocess binaries per invocation, and a capability
 * declaring unknown cost requires human approval before it runs.
 */

export interface CapabilityView {
  id: string
  title: string
  summary: string
  local: boolean
  network: boolean
  external_writes: boolean
  provider: string
  cost_known: boolean
  request_schema?: unknown
  /** The name the agent calls this capability by, so a tool call in a chat
   *  transcript can be traced back to a capability here. */
  tool_name: string
  /** The host's judgement, computed once server-side so every surface agrees. */
  needs_approval: boolean
}

export interface BinaryView {
  name: string
  path: string
  resolved: boolean
}

/** What a module DECLARED it may need. A request, never a grant. */
export interface PermissionsView {
  filesystem_read: string[]
  filesystem_write: string[]
  network: string[]
  credentials: string[]
  paid_providers: string[]
  publish: boolean
  subprocess: BinaryView[]
}

export interface RequirementView {
  kind: string
  name: string
  available: boolean
  detail?: string
}

export interface ModuleView {
  module: string
  name: string
  version: string
  binary: string
  capabilities: CapabilityView[]
  overlays: number
  skills: number
  permissions: PermissionsView
  requirements: RequirementView[]
  warnings: string[]
  /** The host's own findings about this module -- a stale digest, a cost claim
   *  that contradicts a declaration. Kept apart from what the module said about
   *  itself so a reader can tell who is complaining. */
  host_warnings?: string[]
  /** false when the user has turned this module off. It stays listed with its
   *  capabilities, because there has to be something to turn back on. */
  enabled?: boolean
  /** Set when the module could not describe itself. A broken module is
   *  reported rather than hidden, so one bad install does not silently
   *  disappear. */
  error?: string
  /** Install directory name. Present on a module that could not describe
   *  itself, so it can still be removed -- such a module has no module id. */
  dir?: string
}

export interface ArtifactView {
  id: string
  kind: string
  path: string
  root: string
  media_type: string
  bytes: number
  digest: string
  title?: string
}

export interface ExecutionView {
  local: boolean
  network: boolean
  external_writes: boolean
  provider: string
  /** null means UNKNOWN, not free. Rendering it as 0 is how an unpriced
   *  provider call slips past approval. */
  estimated_cost: number | null
  actual_cost: number | null
  artifacts: ArtifactView[]
}

export interface InvokeResult {
  ok: boolean
  module: string
  capability: string
  duration_ms: number
  error?: { code: string; message: string; retryable: boolean; details: Record<string, unknown> }
  execution: ExecutionView
  warnings: string[]
  /** The host's own findings about this invocation, kept separate from the
   *  module's so a reader can tell who is complaining. */
  host_warnings: string[]
  result?: unknown
  stderr?: string
  at: string
}

export async function listModules(): Promise<ModuleView[]> {
  const res = await launcherFetch("/api/modules")
  if (!res.ok) throw new Error(`failed to list modules: ${res.status}`)
  return res.json()
}

/**
 * Where installed modules live on this host.
 *
 * The path is derived from host state the browser cannot see, so telling a
 * first-time user to "drop an executable into the modules directory" is advice
 * nobody can act on without it.
 *
 * Returns "" on failure rather than throwing: not knowing the path is a
 * degraded empty state, not a reason to break the page.
 */
export async function modulesLocation(): Promise<string> {
  try {
    const res = await launcherFetch("/api/modules/location")
    if (!res.ok) return ""
    const body = (await res.json()) as { modules_dir?: string }
    return body.modules_dir ?? ""
  } catch {
    return ""
  }
}

export async function installModule(path: string): Promise<{ module: string }> {
  const res = await launcherFetch("/api/modules/install", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  })
  const body = await res.json()
  if (!res.ok) throw new Error(body?.error ?? `install failed: ${res.status}`)
  return body
}

export async function removeModule(id: string): Promise<void> {
  const res = await launcherFetch(`/api/modules/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error ?? `remove failed: ${res.status}`)
  }
}

/**
 * Turn an installed module off or on without removing it.
 *
 * Sends the DESIRED state rather than a toggle: a toggle races with whatever
 * the page last rendered, so two quick clicks would disagree about the result.
 *
 * Disabling keeps the binary, its state and its declared content on disk, so
 * re-enabling restores what the user had rather than making them re-install.
 */
export async function setModuleEnabled(id: string, enabled: boolean): Promise<void> {
  const res = await launcherFetch(`/api/modules/${encodeURIComponent(id)}/enabled`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ enabled }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error ?? `could not change enabled state: ${res.status}`)
  }
}

/**
 * Run one capability.
 *
 * `approved` says a person clicked "Approve and run" on a capability whose
 * declared effects were shown to them first. It is the only way a consent claim
 * survives into a module request: approval arriving through the agent is
 * stripped by the host, because the model was observed minting
 * `paid_generation_approved` from a chat sentence.
 */
export async function invokeCapability(
  moduleId: string,
  capability: string,
  input: unknown,
  approved = false,
): Promise<InvokeResult> {
  const res = await launcherFetch(`/api/modules/${encodeURIComponent(moduleId)}/invoke`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ capability, input, approved }),
  })
  const body = await res.json()
  if (!res.ok) throw new Error(body?.error ?? `invoke failed: ${res.status}`)
  return body
}

/**
 * How a cost is displayed.
 *
 * null is UNKNOWN and must never render as 0 or "free": the two drive
 * different approval decisions, and collapsing them is how an unpriced call
 * looks safe.
 */
export function formatCost(value: number | null | undefined): string {
  if (value === null || value === undefined) return "UNKNOWN"
  if (value === 0) return "free"
  return value.toFixed(4)
}

/** The declared effects of a capability, in words a person can act on. */
export function describeEffects(cap: CapabilityView): string[] {
  const notes: string[] = []
  if (cap.network) notes.push("reaches the network")
  if (cap.external_writes) notes.push("writes files")
  if (!cap.cost_known) notes.push("cost UNKNOWN — may bill real money")
  else if (cap.provider && cap.provider !== "local") notes.push(`provider ${cap.provider}`)
  return notes
}
