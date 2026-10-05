import i18n from "@/i18n"

export type JsonRecord = Record<string, unknown>

export interface CoreConfigForm {
  workspace: string
  restrictToWorkspace: boolean
  splitOnMarker: boolean
  toolFeedbackEnabled: boolean
  toolFeedbackMaxArgsLength: string
  toolFeedbackSeparateMessages: boolean
  execEnabled: boolean
  allowRemote: boolean
  enableDenyPatterns: boolean
  customDenyPatternsText: string
  customAllowPatternsText: string
  execTimeoutSeconds: string
  allowCommand: boolean
  cronExecTimeoutMinutes: string
  approvalDefault: string
  approvalRules: ApprovalRuleForm[]
  maxTokens: string
  contextWindow: string
  maxToolIterations: string
  summarizeMessageThreshold: string
  summarizeTokenPercent: string
  turnProfile: TurnProfileForm
  dmScope: string
  commandsOwnerOnly: boolean
  heartbeatEnabled: boolean
  heartbeatInterval: string
  devicesEnabled: boolean
  monitorUSB: boolean
  mcpEnabled: boolean
  mcpDiscoveryEnabled: boolean
  mcpDiscoveryTTL: string
  mcpDiscoveryMaxSearchResults: string
  mcpDiscoveryUseBM25: boolean
  mcpDiscoveryUseRegex: boolean
  mcpServers: MCPServerForm[]
  evolutionEnabled: boolean
  evolutionMode: string
  evolutionStateDir: string
  evolutionMinTaskCount: string
  evolutionMinSuccessRatio: string
  evolutionColdPathTrigger: string
  evolutionColdPathTimesText: string
  logRedactSecrets: boolean
  logMaxSizeMB: string
  logMaxFiles: string
}

/** The values tools.approval takes, in the order the form lists them. */
export const APPROVAL_ACTIONS = ["allow", "ask", "deny", "hide"] as const
export const APPROVAL_ORIGINS = ["web", "cli", "chat", "cron"] as const
export const APPROVAL_HINTS = [
  "read_only",
  "destructive",
  "idempotent",
  "open_world",
  "cost_unknown",
  "network",
  "external_writes",
] as const

/** One of tools.approval.rules; the first rule a call matches decides it. */
export interface ApprovalRuleForm {
  id: string
  tool: string
  source: string
  origin: string[]
  hints: string[]
  action: string
  /**
   * A rule holding a value the form does not know, shown read-only and
   * saved back as it came.
   */
  kept?: unknown
}

export type RemoteImages = "click" | "always"

/**
 * What the config API shows instead of a secret map value (MCP env and
 * headers); sending it back keeps the stored value.
 */
export const SECRET_VALUE_PLACEHOLDER = "[NOT_HERE]"

/** How the form shows a stored secret map value: set, but hidden. */
export const MASKED_SECRET_VALUE = "********"

export type MCPServerType = "http" | "sse" | "stdio"

export type TurnProfileMode = "default" | "off" | "custom"

export interface TurnProfileForm {
  enabled: boolean
  historyMode: Exclude<TurnProfileMode, "custom">
  systemPromptMode: Exclude<TurnProfileMode, "custom">
  skillsMode: TurnProfileMode
  skillsAllowText: string
  toolsMode: TurnProfileMode
  toolsAllowText: string
}

export interface MCPServerForm {
  id: string
  name: string
  enabled: boolean
  deferredOverride: boolean | null
  type: MCPServerType
  url: string
  command: string
  argsText: string
  envText: string
  envFile: string
  headersText: string
}

export interface LauncherForm {
  port: string
  publicAccess: boolean
  allowLANWithoutPassword: boolean
  allowedCIDRsText: string
  allowedHostsText: string
  allowLocalhostBypass: boolean
  trustedProxyCIDRsText: string
  remoteImages: RemoteImages
  dashboardPassword: string
  dashboardPasswordConfirm: string
  dashboardPasswordCurrent: string
}

export const DM_SCOPE_OPTIONS = [
  {
    value: "per-channel-peer",
    labelKey: "pages.config.session_scope_per_channel_peer",
    labelDefault: "Per Channel + Peer",
    descKey: "pages.config.session_scope_per_channel_peer_desc",
    descDefault: "Separate context for each user in each channel.",
  },
  {
    value: "per-channel",
    labelKey: "pages.config.session_scope_per_channel",
    labelDefault: "Per Channel",
    descKey: "pages.config.session_scope_per_channel_desc",
    descDefault: "One shared context per channel.",
  },
  {
    value: "per-peer",
    labelKey: "pages.config.session_scope_per_peer",
    labelDefault: "Per Peer",
    descKey: "pages.config.session_scope_per_peer_desc",
    descDefault: "One context per user across channels.",
  },
  {
    value: "global",
    labelKey: "pages.config.session_scope_global",
    labelDefault: "Global",
    descKey: "pages.config.session_scope_global_desc",
    descDefault: "All messages share one global context.",
  },
] as const

export const EMPTY_FORM: CoreConfigForm = {
  workspace: "",
  restrictToWorkspace: true,
  splitOnMarker: false,
  toolFeedbackEnabled: false,
  toolFeedbackMaxArgsLength: "300",
  toolFeedbackSeparateMessages: false,
  execEnabled: true,
  allowRemote: true,
  enableDenyPatterns: true,
  customDenyPatternsText: "",
  customAllowPatternsText: "",
  execTimeoutSeconds: "0",
  allowCommand: true,
  cronExecTimeoutMinutes: "5",
  approvalDefault: "allow",
  approvalRules: [],
  maxTokens: "32768",
  contextWindow: "",
  maxToolIterations: "50",
  summarizeMessageThreshold: "20",
  summarizeTokenPercent: "75",
  turnProfile: {
    enabled: false,
    historyMode: "default",
    systemPromptMode: "default",
    skillsMode: "default",
    skillsAllowText: "",
    toolsMode: "default",
    toolsAllowText: "",
  },
  dmScope: "per-channel-peer",
  commandsOwnerOnly: true,
  heartbeatEnabled: true,
  heartbeatInterval: "30",
  devicesEnabled: false,
  monitorUSB: true,
  mcpEnabled: false,
  mcpDiscoveryEnabled: false,
  mcpDiscoveryTTL: "5",
  mcpDiscoveryMaxSearchResults: "5",
  mcpDiscoveryUseBM25: true,
  mcpDiscoveryUseRegex: false,
  mcpServers: [],
  evolutionEnabled: false,
  evolutionMode: "observe",
  evolutionStateDir: "",
  evolutionMinTaskCount: "2",
  evolutionMinSuccessRatio: "0.7",
  evolutionColdPathTrigger: "after_turn",
  evolutionColdPathTimesText: "",
  logRedactSecrets: true,
  logMaxSizeMB: "10",
  logMaxFiles: "5",
}

export const EMPTY_LAUNCHER_FORM: LauncherForm = {
  port: "18800",
  publicAccess: false,
  allowLANWithoutPassword: false,
  allowedCIDRsText: "",
  allowedHostsText: "",
  allowLocalhostBypass: true,
  trustedProxyCIDRsText: "",
  remoteImages: "click",
  dashboardPassword: "",
  dashboardPasswordConfirm: "",
  dashboardPasswordCurrent: "",
}

function asRecord(value: unknown): JsonRecord {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as JsonRecord
  }
  return {}
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asBool(value: unknown): boolean {
  return value === true
}

function asOptionalBool(value: unknown): boolean | null {
  return typeof value === "boolean" ? value : null
}

function asNumberString(value: unknown, fallback: string): string {
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value)
  }
  if (typeof value === "string" && value.trim() !== "") {
    return value
  }
  return fallback
}

function toMCPServerType(value: unknown): MCPServerType {
  if (value === "http" || value === "sse") {
    return value
  }
  return "stdio"
}

function makeMCPServerID(name: string): string {
  const encoded = encodeURIComponent(name)
  if (encoded.length > 0) {
    return `mcp-${encoded}`
  }
  return `mcp-${Math.random().toString(36).slice(2, 10)}`
}

/** Shows each stored secret value of an env or headers map as masked. */
function maskSecretValues(map: JsonRecord): JsonRecord {
  return Object.fromEntries(
    Object.entries(map).map(([key, value]) => [
      key,
      value === SECRET_VALUE_PLACEHOLDER ? MASKED_SECRET_VALUE : value,
    ]),
  )
}

/**
 * Turns the values left masked back into the placeholder, so that saving
 * keeps the stored secrets; edited values are sent as typed.
 */
export function unmaskSecretValues(
  map: Record<string, string>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(map).map(([key, value]) => [
      key,
      value === MASKED_SECRET_VALUE ? SECRET_VALUE_PLACEHOLDER : value,
    ]),
  )
}

function mapMCPServers(value: unknown): MCPServerForm[] {
  const servers = asRecord(value)
  return Object.entries(servers).map(([name, rawConfig]) => {
    const cfg = asRecord(rawConfig)
    const argsList = Array.isArray(cfg.args)
      ? cfg.args.filter((item): item is string => typeof item === "string")
      : []
    const url = asString(cfg.url)
    const type =
      cfg.type === undefined
        ? url
          ? "sse"
          : "stdio"
        : toMCPServerType(cfg.type)
    const env = maskSecretValues(asRecord(cfg.env))
    const headers = maskSecretValues(asRecord(cfg.headers))

    return {
      id: makeMCPServerID(name),
      name,
      enabled: cfg.enabled !== false,
      deferredOverride: asOptionalBool(cfg.deferred),
      type,
      url,
      command: asString(cfg.command),
      argsText: argsList.join("\n"),
      envText: JSON.stringify(env, null, 2),
      envFile: asString(cfg.env_file),
      headersText: JSON.stringify(headers, null, 2),
    }
  })
}

function toTurnProfileMode(value: unknown): TurnProfileMode {
  if (value === "off" || value === "custom") {
    return value
  }
  return "default"
}

function toBasicTurnProfileMode(
  value: unknown,
): Exclude<TurnProfileMode, "custom"> {
  return value === "off" ? "off" : "default"
}

function allowListText(value: unknown): string {
  if (!Array.isArray(value)) {
    return ""
  }
  return value
    .filter((item): item is string => typeof item === "string")
    .join("\n")
}

function mapTurnProfile(value: unknown): TurnProfileForm {
  const profile = asRecord(value)
  const history = asRecord(profile.history)
  const systemPrompt = asRecord(profile.system_prompt)
  const skills = asRecord(profile.skills)
  const tools = asRecord(profile.tools)

  return {
    enabled: asBool(profile.enabled),
    historyMode: toBasicTurnProfileMode(history.mode),
    systemPromptMode: toBasicTurnProfileMode(systemPrompt.mode),
    skillsMode: toTurnProfileMode(skills.mode),
    skillsAllowText: allowListText(skills.allow),
    toolsMode: toTurnProfileMode(tools.mode),
    toolsAllowText: allowListText(tools.allow),
  }
}

function isOneOf(options: readonly string[], value: unknown): boolean {
  return typeof value === "string" && options.includes(value)
}

/** Whether value is unset, or a list of these options only. */
function isOptionalList(value: unknown, options: readonly string[]): boolean {
  return (
    value === undefined ||
    (Array.isArray(value) && value.every((item) => isOneOf(options, item)))
  )
}

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : []
}

const APPROVAL_RULE_FIELDS = ["tool", "source", "origin", "hints", "action"]

/** Whether the form can show, and so edit, a rule of tools.approval. */
function isKnownApprovalRule(value: unknown): boolean {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false
  }
  const rule = value as JsonRecord
  return (
    Object.keys(rule).every((key) => APPROVAL_RULE_FIELDS.includes(key)) &&
    (rule.tool === undefined || typeof rule.tool === "string") &&
    (rule.source === undefined || typeof rule.source === "string") &&
    isOptionalList(rule.origin, APPROVAL_ORIGINS) &&
    isOptionalList(rule.hints, APPROVAL_HINTS) &&
    isOneOf(APPROVAL_ACTIONS, rule.action)
  )
}

/**
 * The rules of tools.approval, in their order. A rule with a field or a
 * value the form does not know is kept as it came, so saving cannot change
 * what it does.
 */
function mapApprovalRules(value: unknown): ApprovalRuleForm[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value.map((raw: unknown, index) => {
    const rule = asRecord(raw)
    const form: ApprovalRuleForm = {
      id: `approval-rule-${index}`,
      tool: asString(rule.tool),
      source: asString(rule.source),
      origin: stringList(rule.origin),
      hints: stringList(rule.hints),
      action: asString(rule.action),
    }
    return isKnownApprovalRule(raw) ? form : { ...form, kept: raw }
  })
}

export function buildFormFromConfig(config: unknown): CoreConfigForm {
  const root = asRecord(config)
  const agents = asRecord(root.agents)
  const defaults = asRecord(agents.defaults)
  const session = asRecord(root.session)
  const commands = asRecord(root.commands)
  const logging = asRecord(root.logging)
  const heartbeat = asRecord(root.heartbeat)
  const devices = asRecord(root.devices)
  const evolution = asRecord(root.evolution)
  const tools = asRecord(root.tools)
  const mcp = asRecord(tools.mcp)
  const mcpDiscovery = asRecord(mcp.discovery)
  const cron = asRecord(tools.cron)
  const exec = asRecord(tools.exec)
  const approval = asRecord(tools.approval)
  const toolFeedback = asRecord(defaults.tool_feedback)

  return {
    workspace: asString(defaults.workspace) || EMPTY_FORM.workspace,
    restrictToWorkspace:
      defaults.restrict_to_workspace === undefined
        ? EMPTY_FORM.restrictToWorkspace
        : asBool(defaults.restrict_to_workspace),
    splitOnMarker:
      defaults.split_on_marker === undefined
        ? EMPTY_FORM.splitOnMarker
        : asBool(defaults.split_on_marker),
    toolFeedbackEnabled:
      toolFeedback.enabled === undefined
        ? EMPTY_FORM.toolFeedbackEnabled
        : asBool(toolFeedback.enabled),
    toolFeedbackMaxArgsLength: asNumberString(
      toolFeedback.max_args_length,
      EMPTY_FORM.toolFeedbackMaxArgsLength,
    ),
    toolFeedbackSeparateMessages:
      toolFeedback.separate_messages === undefined
        ? EMPTY_FORM.toolFeedbackSeparateMessages
        : asBool(toolFeedback.separate_messages),
    execEnabled:
      exec.enabled === undefined
        ? EMPTY_FORM.execEnabled
        : asBool(exec.enabled),
    allowRemote:
      exec.allow_remote === undefined
        ? EMPTY_FORM.allowRemote
        : asBool(exec.allow_remote),
    enableDenyPatterns:
      exec.enable_deny_patterns === undefined
        ? EMPTY_FORM.enableDenyPatterns
        : asBool(exec.enable_deny_patterns),
    customDenyPatternsText: Array.isArray(exec.custom_deny_patterns)
      ? exec.custom_deny_patterns
          .filter((value): value is string => typeof value === "string")
          .join("\n")
      : EMPTY_FORM.customDenyPatternsText,
    customAllowPatternsText: Array.isArray(exec.custom_allow_patterns)
      ? exec.custom_allow_patterns
          .filter((value): value is string => typeof value === "string")
          .join("\n")
      : EMPTY_FORM.customAllowPatternsText,
    execTimeoutSeconds: asNumberString(
      exec.timeout_seconds,
      EMPTY_FORM.execTimeoutSeconds,
    ),
    allowCommand:
      cron.allow_command === undefined
        ? EMPTY_FORM.allowCommand
        : asBool(cron.allow_command),
    cronExecTimeoutMinutes: asNumberString(
      cron.exec_timeout_minutes,
      EMPTY_FORM.cronExecTimeoutMinutes,
    ),
    // An unset default allows.
    approvalDefault: asString(approval.default) || EMPTY_FORM.approvalDefault,
    approvalRules: mapApprovalRules(approval.rules),
    maxTokens: asNumberString(defaults.max_tokens, EMPTY_FORM.maxTokens),
    contextWindow: asNumberString(
      defaults.context_window,
      EMPTY_FORM.contextWindow,
    ),
    maxToolIterations: asNumberString(
      defaults.max_tool_iterations,
      EMPTY_FORM.maxToolIterations,
    ),
    summarizeMessageThreshold: asNumberString(
      defaults.summarize_message_threshold,
      EMPTY_FORM.summarizeMessageThreshold,
    ),
    summarizeTokenPercent: asNumberString(
      defaults.summarize_token_percent,
      EMPTY_FORM.summarizeTokenPercent,
    ),
    turnProfile: mapTurnProfile(defaults.turn_profile),
    dmScope: asString(session.dm_scope) || EMPTY_FORM.dmScope,
    commandsOwnerOnly:
      commands.owner_only === undefined
        ? EMPTY_FORM.commandsOwnerOnly
        : asBool(commands.owner_only),
    heartbeatEnabled:
      heartbeat.enabled === undefined
        ? EMPTY_FORM.heartbeatEnabled
        : asBool(heartbeat.enabled),
    heartbeatInterval: asNumberString(
      heartbeat.interval,
      EMPTY_FORM.heartbeatInterval,
    ),
    devicesEnabled:
      devices.enabled === undefined
        ? EMPTY_FORM.devicesEnabled
        : asBool(devices.enabled),
    monitorUSB:
      devices.monitor_usb === undefined
        ? EMPTY_FORM.monitorUSB
        : asBool(devices.monitor_usb),
    mcpEnabled:
      mcp.enabled === undefined ? EMPTY_FORM.mcpEnabled : asBool(mcp.enabled),
    mcpDiscoveryEnabled:
      mcpDiscovery.enabled === undefined
        ? EMPTY_FORM.mcpDiscoveryEnabled
        : asBool(mcpDiscovery.enabled),
    mcpDiscoveryTTL: asNumberString(
      mcpDiscovery.ttl,
      EMPTY_FORM.mcpDiscoveryTTL,
    ),
    mcpDiscoveryMaxSearchResults: asNumberString(
      mcpDiscovery.max_search_results,
      EMPTY_FORM.mcpDiscoveryMaxSearchResults,
    ),
    mcpDiscoveryUseBM25:
      mcpDiscovery.use_bm25 === undefined
        ? EMPTY_FORM.mcpDiscoveryUseBM25
        : asBool(mcpDiscovery.use_bm25),
    mcpDiscoveryUseRegex:
      mcpDiscovery.use_regex === undefined
        ? EMPTY_FORM.mcpDiscoveryUseRegex
        : asBool(mcpDiscovery.use_regex),
    mcpServers: mapMCPServers(mcp.servers),
    evolutionEnabled:
      evolution.enabled === undefined
        ? EMPTY_FORM.evolutionEnabled
        : asBool(evolution.enabled),
    evolutionMode: asString(evolution.mode) || EMPTY_FORM.evolutionMode,
    evolutionStateDir:
      asString(evolution.state_dir) || EMPTY_FORM.evolutionStateDir,
    evolutionMinTaskCount: asNumberString(
      evolution.min_task_count,
      EMPTY_FORM.evolutionMinTaskCount,
    ),
    evolutionMinSuccessRatio: asNumberString(
      evolution.min_success_ratio,
      EMPTY_FORM.evolutionMinSuccessRatio,
    ),
    evolutionColdPathTrigger:
      asString(evolution.cold_path_trigger) ||
      EMPTY_FORM.evolutionColdPathTrigger,
    evolutionColdPathTimesText: Array.isArray(evolution.cold_path_times)
      ? evolution.cold_path_times
          .filter((value): value is string => typeof value === "string")
          .join("\n")
      : EMPTY_FORM.evolutionColdPathTimesText,
    logRedactSecrets:
      logging.redact_secrets === undefined
        ? EMPTY_FORM.logRedactSecrets
        : asBool(logging.redact_secrets),
    // 0 means the default, which the form shows as the number it is.
    logMaxSizeMB: positiveNumberString(
      logging.max_size_mb,
      EMPTY_FORM.logMaxSizeMB,
    ),
    logMaxFiles: positiveNumberString(
      logging.max_files,
      EMPTY_FORM.logMaxFiles,
    ),
  }
}

function positiveNumberString(value: unknown, fallback: string): string {
  return typeof value === "number" && value > 0 ? String(value) : fallback
}

function checkRange(
  value: number,
  label: string,
  options: { min?: number; max?: number },
) {
  if (options.min !== undefined && value < options.min) {
    throw new Error(
      i18n.t("pages.config.errors.min", { label, min: options.min }),
    )
  }
  if (options.max !== undefined && value > options.max) {
    throw new Error(
      i18n.t("pages.config.errors.max", { label, max: options.max }),
    )
  }
}

export function parseIntField(
  rawValue: string,
  label: string,
  options: { min?: number; max?: number } = {},
): number {
  const value = Number(rawValue)
  if (!Number.isInteger(value)) {
    throw new Error(i18n.t("pages.config.errors.integer", { label }))
  }
  checkRange(value, label, options)
  return value
}

export function parseFloatField(
  rawValue: string,
  label: string,
  options: { min?: number; max?: number } = {},
): number {
  // A decimal comma is how many locales write 0.7, so it is read as a point.
  const value = Number(rawValue.trim().replace(",", "."))
  if (!Number.isFinite(value)) {
    throw new Error(i18n.t("pages.config.errors.number", { label }))
  }
  checkRange(value, label, options)
  return value
}

export function parseCIDRText(raw: string): string[] {
  if (!raw.trim()) {
    return []
  }
  return raw
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter((v) => v.length > 0)
}

export function parseMultilineList(raw: string): string[] {
  if (!raw.trim()) {
    return []
  }
  return raw
    .split("\n")
    .map((value) => value.trim())
    .filter((value) => value.length > 0)
}

export function parseJSONObjectField(
  rawValue: string,
  label: string,
): Record<string, string> {
  const trimmed = rawValue.trim()
  if (trimmed === "") {
    return {}
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    throw new Error(i18n.t("pages.config.errors.json", { label }))
  }

  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(i18n.t("pages.config.errors.json_object", { label }))
  }

  const entries = Object.entries(parsed as Record<string, unknown>)
  const result: Record<string, string> = {}
  for (const [key, value] of entries) {
    if (typeof value !== "string") {
      throw new Error(i18n.t("pages.config.errors.json_string", { label, key }))
    }
    result[key] = value
  }
  return result
}
