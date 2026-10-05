import * as React from "react"
import { Trans, useTranslation } from "react-i18next"

import {
  type CapabilityView,
  type InvokeResult,
  type ModuleView,
  describeEffects,
  formatCost,
  installModule,
  invokeCapability,
  listModules,
  modulesLocation,
  removeModule,
  setModuleEnabled,
} from "@/api/modules"
import { PageHeader } from "@/components/page-header"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

/**
 * Modules page.
 *
 * Modules are detached local processes that contribute capabilities to the
 * agent. This page is where they are installed, inspected, tried, and removed.
 *
 * Two presentation rules here are load-bearing rather than cosmetic:
 *
 *   - a cost that is unknown reads UNKNOWN, never 0 or "free". The two drive
 *     different approval decisions, and collapsing them is how an unpriced
 *     provider call looks safe.
 *   - what a module DECLARED it may need is shown as a request, not a grant.
 *     A module runs under the user's account; the host names roots per
 *     invocation but does not confine the process to them.
 */
export function ModulesPage() {
  const { t } = useTranslation()
  const [modules, setModules] = React.useState<ModuleView[]>([])
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState("")
  const [installPath, setInstallPath] = React.useState("")
  const [modulesDir, setModulesDir] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [pendingRemoval, setPendingRemoval] = React.useState("")

  const refresh = React.useCallback(async () => {
    setLoading(true)
    try {
      setModules(await listModules())
      setError("")
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    void refresh()
  }, [refresh])

  // Where modules live is host state the browser cannot derive, and the empty
  // state below is useless without it.
  React.useEffect(() => {
    void modulesLocation().then(setModulesDir)
  }, [])

  const handleInstall = async () => {
    if (!installPath.trim()) return
    setBusy(true)
    try {
      await installModule(installPath.trim())
      setInstallPath("")
      setError("")
      await refresh()
    } catch (err) {
      // Refusal reasons are shown verbatim: "this binary does not speak the
      // protocol" is the answer someone needs, not a generic failure.
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const handleSetEnabled = async (id: string, enabled: boolean) => {
    setBusy(true)
    try {
      await setModuleEnabled(id, enabled)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const handleRemove = async (id: string) => {
    setBusy(true)
    try {
      await removeModule(id)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <PageHeader title={t("navigation.modules")} />
      <div className="flex flex-col gap-6 p-4 md:p-6">
        <section className="flex flex-col gap-2">
          <p className="text-muted-foreground text-sm">
            {t("pages.agent.modules.intro")}
          </p>
          <div className="flex gap-2">
            <Input
              value={installPath}
              onChange={(e) => setInstallPath(e.target.value)}
              placeholder={t("pages.agent.modules.install_placeholder")}
              aria-label={t("pages.agent.modules.install_placeholder")}
              disabled={busy}
              onKeyDown={(e) => {
                if (e.key === "Enter") void handleInstall()
              }}
            />
            <Button
              onClick={() => void handleInstall()}
              disabled={busy || !installPath.trim()}
            >
              {t("pages.agent.modules.install")}
            </Button>
            <Button
              variant="outline"
              onClick={() => void refresh()}
              disabled={busy}
            >
              {t("pages.agent.modules.refresh")}
            </Button>
          </div>
          {error && (
            <p className="text-destructive font-mono text-xs whitespace-pre-wrap">
              {error}
            </p>
          )}
        </section>

        {loading && (
          <p className="text-muted-foreground text-sm">
            {t("pages.agent.modules.discovering")}
          </p>
        )}

        {!loading && modules.length === 0 && (
          <div className="text-muted-foreground flex flex-col gap-1 text-sm">
            <p>{t("pages.agent.modules.empty")}</p>
            <p>
              {modulesDir ? (
                <Trans
                  i18nKey="pages.agent.modules.empty_install_dir"
                  values={{ dir: modulesDir }}
                  components={{ dir: <span className="font-mono text-xs" /> }}
                />
              ) : (
                t("pages.agent.modules.empty_install")
              )}
            </p>
          </div>
        )}

        {modules.map((m) => (
          <ModuleCard
            key={m.module || m.binary}
            module={m}
            onRemove={setPendingRemoval}
            onSetEnabled={handleSetEnabled}
            busy={busy}
          />
        ))}
      </div>
      <AlertDialog
        open={pendingRemoval !== ""}
        onOpenChange={(open) => {
          if (!open) setPendingRemoval("")
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("pages.agent.modules.remove_title")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("pages.agent.modules.remove_description", {
                id: pendingRemoval,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                const id = pendingRemoval
                setPendingRemoval("")
                void handleRemove(id)
              }}
            >
              {t("pages.agent.modules.remove_confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function ModuleCard({
  module: m,
  onRemove,
  onSetEnabled,
  busy,
}: {
  module: ModuleView
  onRemove: (id: string) => void
  onSetEnabled: (id: string, enabled: boolean) => void
  busy: boolean
}) {
  const { t } = useTranslation()

  if (m.error) {
    // A broken module is reported, never hidden: one bad install must not
    // quietly disappear from the list.
    //
    // It also has to be REMOVABLE. A module that could not describe itself has
    // no module id -- the id comes from the descriptor it failed to produce --
    // so the card removes it by install directory instead. Without that the
    // cockpit could show the breakage and offer no way to clear it, and the
    // only fix was deleting a folder the UI never named.
    return (
      <section className="border-destructive/60 rounded-lg border p-4">
        <header className="flex items-start justify-between gap-3">
          <h2 className="font-mono text-sm font-semibold">{m.binary}</h2>
          {m.dir && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => onRemove(m.dir!)}
              disabled={busy}
              title={t("pages.agent.modules.remove_broken_hint")}
            >
              {t("pages.agent.modules.remove")}
            </Button>
          )}
        </header>
        <p className="text-destructive mt-1 font-mono text-xs whitespace-pre-wrap">
          {m.error}
        </p>
      </section>
    )
  }

  const unavailableRequirements = m.requirements.filter((r) => !r.available)
  const diagnosticCount =
    (m.host_warnings?.length ?? 0) +
    m.warnings.length +
    unavailableRequirements.length

  return (
    <section className="border-border/60 flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-start justify-between gap-3">
        <div>
          <h2 className="font-mono text-sm font-semibold">{m.module}</h2>
          <p className="text-muted-foreground text-xs">
            {m.name} · v{m.version} ·{" "}
            {t("chat.module.capabilities", { count: m.capabilities.length })}
            {m.overlays > 0 &&
              ` · ${t("pages.agent.modules.overlays", { count: m.overlays })}`}
            {m.skills > 0 &&
              ` · ${t("pages.agent.modules.skills", { count: m.skills })}`}
          </p>
          {m.enabled === false && (
            <p className="text-muted-foreground mt-1 text-xs">
              {t("pages.agent.modules.disabled_note")}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2">
          {/* Disabling is the middle state between installed and removed: the
              module's capabilities stop being offered as agent tools, freeing
              that budget, while the module and its state stay in place. */}
          <Button
            variant="outline"
            size="sm"
            onClick={() => onSetEnabled(m.module, m.enabled === false)}
            disabled={busy}
            title={
              m.enabled === false
                ? t("pages.agent.modules.enable_hint")
                : t("pages.agent.modules.disable_hint")
            }
          >
            {m.enabled === false
              ? t("pages.agent.modules.enable")
              : t("pages.agent.modules.disable")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onRemove(m.module)}
            disabled={busy}
            title={t("pages.agent.modules.remove_hint")}
          >
            {t("pages.agent.modules.remove")}
          </Button>
        </div>
      </header>

      {/* The host's findings first, and labelled. A stale digest found here is
          something the operator can act on; a module's own diagnostic may be
          about its source tree and not about this install at all. Merged, they
          looked identical. */}
      {diagnosticCount > 0 && (
        <details className="border-border/60 bg-muted/20 rounded-lg border px-3 py-2">
          <summary className="text-muted-foreground hover:text-foreground cursor-pointer text-xs font-medium">
            {t("pages.agent.modules.setup_issues", { count: diagnosticCount })}
          </summary>
          <div className="mt-3 space-y-2">
            {(m.host_warnings ?? []).map((w, i) => (
              <p key={`host-${i}`} className="text-xs text-amber-500">
                <span className="font-medium">
                  {t("pages.agent.modules.host_label")}
                </span>{" "}
                {w}
              </p>
            ))}
            {m.warnings.map((w, i) => (
              <p key={i} className="text-muted-foreground text-xs">
                <span className="font-medium">
                  {t("pages.agent.modules.module_label")}
                </span>{" "}
                {w}
              </p>
            ))}
            {unavailableRequirements.map((r, i) => (
              <p key={i} className="text-xs text-amber-500">
                {t("pages.agent.modules.missing_requirement", {
                  kind: r.kind,
                })}{" "}
                <span className="font-mono">{r.name}</span>
                {r.detail ? ` — ${r.detail}` : ""}
              </p>
            ))}
          </div>
        </details>
      )}

      <details className="border-border/50 rounded-lg border px-3 py-2">
        <summary className="text-muted-foreground hover:text-foreground cursor-pointer text-xs font-medium">
          {t("pages.agent.modules.declared_permissions")}
        </summary>
        <div className="mt-2">
          <Declared permissions={m.permissions} />
        </div>
      </details>

      <div className="divide-border/40 flex flex-col divide-y">
        {m.capabilities.map((c) => (
          <Capability key={c.id} moduleId={m.module} capability={c} />
        ))}
      </div>
    </section>
  )
}

/** What a module said it may need. A request, never a grant. */
function Declared({
  permissions: p,
}: {
  permissions: ModuleView["permissions"]
}) {
  const { t } = useTranslation()
  const rows: Array<[string, string]> = []
  if (p.filesystem_read.length)
    rows.push([
      t("pages.agent.modules.permission_reads"),
      p.filesystem_read.join(" "),
    ])
  if (p.filesystem_write.length)
    rows.push([
      t("pages.agent.modules.permission_writes"),
      p.filesystem_write.join(" "),
    ])
  if (p.network.length)
    rows.push([
      t("pages.agent.modules.permission_network"),
      p.network.join(" "),
    ])
  if (p.credentials.length)
    rows.push([
      t("pages.agent.modules.permission_credentials"),
      p.credentials.join(" "),
    ])
  if (p.paid_providers.length)
    rows.push([
      t("pages.agent.modules.permission_paid_providers"),
      p.paid_providers.join(" "),
    ])
  if (p.publish)
    rows.push([
      t("pages.agent.modules.permission_publish"),
      t("pages.agent.modules.permission_yes"),
    ])

  const unresolved = p.subprocess.filter((b) => !b.resolved)

  if (rows.length === 0 && p.subprocess.length === 0) return null

  return (
    <div className="bg-muted/30 rounded-md p-3 text-xs">
      <p className="text-muted-foreground mb-1.5">
        {t("pages.agent.modules.declared_intro")}
      </p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono">
        {rows.map(([k, v]) => (
          <React.Fragment key={k}>
            <dt className="text-muted-foreground">{k}</dt>
            <dd>{v}</dd>
          </React.Fragment>
        ))}
        {p.subprocess.length > 0 && (
          <>
            <dt className="text-muted-foreground">
              {t("pages.agent.modules.permission_binaries")}
            </dt>
            <dd>
              {t("pages.agent.modules.binaries_resolved", {
                resolved: p.subprocess.length - unresolved.length,
                total: p.subprocess.length,
              })}
              {unresolved.length > 0 && (
                <span className="text-destructive">
                  {" — "}
                  {t("pages.agent.modules.binaries_missing", {
                    names: unresolved.map((b) => b.name).join(" "),
                  })}
                </span>
              )}
              {/* Naming them matters: the module picks from this list, and the
                  host cannot tell a signed-in CLI from an installed one. An
                  operator seeing "3/3 resolved" next to a capability that fails
                  on authentication has no way to know which one it reached
                  for. */}
              {p.subprocess.length - unresolved.length > 0 && (
                <div className="text-muted-foreground mt-1 text-[11px]">
                  <Trans
                    i18nKey="pages.agent.modules.binaries_note"
                    values={{
                      names: p.subprocess
                        .filter((b) => b.resolved)
                        .map((b) => b.name)
                        .join(", "),
                    }}
                    components={{
                      code: (
                        <code className="bg-muted mx-1 rounded px-1 py-0.5 font-mono" />
                      ),
                    }}
                  />
                </div>
              )}
            </dd>
          </>
        )}
      </dl>
    </div>
  )
}

function Capability({
  moduleId,
  capability: c,
}: {
  moduleId: string
  capability: CapabilityView
}) {
  const { t } = useTranslation()
  const [open, setOpen] = React.useState(false)
  const [input, setInput] = React.useState("{}")
  const [result, setResult] = React.useState<InvokeResult | null>(null)
  const [running, setRunning] = React.useState(false)

  const effects = describeEffects(c, t)

  const run = async () => {
    let parsed: unknown
    try {
      parsed = JSON.parse(input || "{}")
    } catch (err) {
      window.alert(
        t("pages.agent.modules.invalid_input", {
          error: err instanceof Error ? err.message : String(err),
        }),
      )
      return
    }
    setRunning(true)
    try {
      // The approval policy (tools.approval) decides whether this capability
      // needs approval. When it asks, pressing "Approve and run" IS the
      // answer, and the page said so before the click. That is a person
      // acting on what they were shown -- unlike an approval claim arriving
      // through the agent, which the host strips.
      setResult(
        await invokeCapability(moduleId, c.id, parsed, c.needs_approval),
      )
    } catch (err) {
      window.alert(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="py-2.5">
      <button
        type="button"
        aria-expanded={open}
        className="flex w-full items-start justify-between gap-3 text-left"
        onClick={() => setOpen(!open)}
      >
        <div className="min-w-0">
          <p className="font-mono text-xs">{c.id}</p>
          <p className="text-muted-foreground text-xs">{c.summary}</p>
          {effects.length > 0 && (
            <p
              className={`text-xs ${c.cost_known ? "text-muted-foreground" : "text-destructive"}`}
            >
              {effects.join(" · ")}
            </p>
          )}
        </div>
        <span className="text-muted-foreground shrink-0 font-mono text-[11px]">
          {c.tool_name}
        </span>
      </button>

      {open && (
        <div className="mt-2 flex flex-col gap-2">
          {/* Pressing this button is what records consent against the
              declared effects below; consent claims the model writes itself
              are stripped by the host. Saying only "running this is your
              approval" understated it once the click began authorizing a paid
              provider call. */}
          {c.needs_approval && (
            <div className="rounded border border-amber-500/60 bg-amber-500/5 p-2 text-xs text-amber-500">
              <p>
                {t("pages.agent.modules.declared_effects", {
                  effects: effects.join("; "),
                })}
              </p>
              <p className="mt-1">
                <Trans
                  i18nKey={
                    c.cost_known
                      ? "pages.agent.modules.approval_note"
                      : "pages.agent.modules.approval_note_cost"
                  }
                  components={{ b: <span className="font-medium" /> }}
                />
              </p>
            </div>
          )}
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            aria-label={c.id}
            spellCheck={false}
            rows={4}
            className="border-border/60 bg-muted/20 w-full rounded-md border p-2 font-mono text-xs"
          />
          <div>
            <Button size="sm" onClick={() => void run()} disabled={running}>
              {running
                ? t("pages.agent.modules.running")
                : c.needs_approval
                  ? t("pages.agent.modules.approve_and_run")
                  : t("pages.agent.modules.run")}
            </Button>
          </div>
          {result && <Result result={result} />}
        </div>
      )}
    </div>
  )
}

function Result({ result: r }: { result: InvokeResult }) {
  const { t } = useTranslation()
  const x = r.execution
  return (
    <div
      className={`rounded-md border p-2.5 text-xs ${
        r.ok ? "border-emerald-600/40" : "border-destructive/60"
      }`}
    >
      <p className="font-mono">
        {r.ok
          ? t("pages.agent.modules.result_ok")
          : t("pages.agent.modules.result_failed")}{" "}
        · {r.duration_ms}ms
      </p>
      {r.error && (
        <p className="text-destructive mt-1 font-mono">
          {r.error.code}: {r.error.message}
        </p>
      )}
      <p className="text-muted-foreground mt-1 font-mono">
        local={String(x.local)} network={String(x.network)} writes=
        {String(x.external_writes)}
        {x.provider ? ` provider=${x.provider}` : ""}
      </p>
      <p className="mt-0.5 font-mono">
        <span className="text-muted-foreground">cost </span>
        <span className={x.estimated_cost === null ? "text-destructive" : ""}>
          estimated={formatCost(x.estimated_cost, t)}
        </span>{" "}
        <span className={x.actual_cost === null ? "text-destructive" : ""}>
          actual={formatCost(x.actual_cost, t)}
        </span>
      </p>
      {r.warnings.map((w, i) => (
        <p key={i} className="mt-0.5 text-amber-500">
          {t("pages.agent.modules.result_warning")} {w}
        </p>
      ))}
      {r.host_warnings.map((w, i) => (
        <p key={i} className="text-destructive mt-0.5">
          {t("pages.agent.modules.result_host")} {w}
        </p>
      ))}
      {x.artifacts.map((a) => (
        <div
          key={a.id}
          className="border-border/40 mt-2 rounded border p-2 font-mono"
        >
          <p className="text-primary">
            {a.id} · {a.kind}
          </p>
          <p className="text-muted-foreground break-all">
            {t("pages.agent.modules.artifact_location", {
              path: a.path,
              root: a.root,
              bytes: a.bytes,
            })}
          </p>
          <p className="text-muted-foreground break-all">{a.digest}</p>
        </div>
      ))}
      {r.result !== undefined && r.result !== null && (
        <details className="mt-2">
          <summary className="text-muted-foreground cursor-pointer">
            {t("pages.agent.modules.result")}
          </summary>
          <pre className="bg-muted/30 mt-1 max-h-64 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
            {JSON.stringify(r.result, null, 2).slice(0, 6000)}
          </pre>
        </details>
      )}
      {r.stderr && (
        <details className="mt-1">
          <summary className="text-muted-foreground cursor-pointer">
            {t("pages.agent.modules.diagnostics")}
          </summary>
          <pre className="bg-muted/30 mt-1 max-h-48 overflow-auto rounded p-2 font-mono text-[11px] whitespace-pre-wrap">
            {r.stderr.slice(0, 4000)}
          </pre>
        </details>
      )}
    </div>
  )
}
