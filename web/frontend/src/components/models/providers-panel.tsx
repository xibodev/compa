import {
  IconPlug,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconSparkles,
  IconTrash,
  IconX,
} from "@tabler/icons-react"
import dayjs from "dayjs"
import {
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ExtensionConnectionStatus,
  type ExtensionProvider,
  extensionProviderState,
} from "@/api/extension"
import {
  type PingResult,
  type ProviderInstance,
  type ProviderInstanceCatalog,
  type ProviderInstanceInput,
  type ProviderRosterEntry,
  type ProviderTarget,
  addActiveModel,
  autoConnectFreeProviders,
  createProviderInstance,
  deleteProviderInstance,
  getActiveModels,
  pingProviderInstance,
  removeActiveModel,
  syncProviderCatalog,
  updateProviderInstance,
} from "@/api/provider-instances"
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
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { DefaultModelState } from "@/hooks/use-default-model"
import { formatModelLabel, selectionLabel } from "@/lib/model-labels"
import { cn } from "@/lib/utils"

import { DefaultModelAction } from "./default-model"
import { ExtensionProviderActions } from "./extension-provider-actions"
import { ExtensionSection } from "./extension-section"
import { InstanceDialog, type InstanceDialogTarget } from "./instance-dialog"
import { InstanceModels } from "./instance-models"
import { ProviderIcon } from "./provider-icon"
import {
  type InstanceStatus,
  type ProviderCard,
  SHELF_ORDER,
  type ShelfKey,
  type StoredFreeTest,
  buildProviderCards,
  cardShelf,
  cardStatus,
  clearFreeTest,
  instanceCredentialKind,
  instanceDisplayName,
  instanceStatus,
  instanceSurfaces,
  isCardConnected,
  isExtensionManaged,
  isKnownSurface,
  loadFreeTest,
  saveFreeTest,
  surfacesText,
} from "./provider-model"
import { useExtensionStatus } from "./use-extension-status"

const errorText = (cause: unknown, fallback: string) =>
  cause instanceof Error && cause.message ? cause.message : fallback

const STATUS_CLASS: Record<InstanceStatus, string> = {
  connected: "text-emerald-600 dark:text-emerald-400",
  needs_sign_in: "text-amber-600 dark:text-amber-400",
  needs_token: "text-amber-600 dark:text-amber-400",
  needs_key: "text-amber-600 dark:text-amber-400",
  disabled: "text-muted-foreground",
}

// Why the free provider test could not add a provider, each with a label
// under models.freeTest.status.
const FREE_TEST_REASONS: ReadonlySet<string> = new Set([
  "rate_limited",
  "auth_required",
  "forbidden",
  "probe_failed",
  "no_model",
])

function nextFreeId(base: string, taken: readonly string[]): string {
  const clean = base.trim() || "provider"
  if (!taken.includes(clean)) return clean
  for (let n = 2; ; n += 1) {
    const candidate = `${clean}-${n}`
    if (!taken.includes(candidate)) return candidate
  }
}

type FilterKey = "all" | "connected" | "free" | "apikey" | "local"

interface ProvidersPanelProps {
  instances: ProviderInstance[]
  catalogs: ProviderInstanceCatalog[]
  targets: ProviderTarget[]
  roster: ProviderRosterEntry[]
  refresh: () => Promise<void>
  defaultModel: DefaultModelState
}

/**
 * The provider list and the models in Chat. The list is the one place a
 * provider is added: a card opens its connect dialog, and a connected card
 * opens its details.
 */
export function ProvidersPanel({
  instances,
  catalogs,
  targets,
  roster,
  refresh,
  defaultModel,
}: ProvidersPanelProps) {
  const { t } = useTranslation()
  const [search, setSearch] = useState("")
  const [filter, setFilter] = useState<FilterKey>("all")
  const [expandedId, setExpandedId] = useState("")
  const [selectedInstance, setSelectedInstance] = useState<
    Record<string, string>
  >({})
  const [pingingId, setPingingId] = useState<string | null>(null)
  const [syncingId, setSyncingId] = useState<string | null>(null)
  const [pingResults, setPingResults] = useState<Record<string, PingResult>>({})
  const [autoConnecting, setAutoConnecting] = useState(false)
  const [freeTest, setFreeTest] = useState<StoredFreeTest | null>(loadFreeTest)
  const [pendingDelete, setPendingDelete] = useState<ProviderInstance | null>(
    null,
  )
  const [activeModels, setActiveModels] = useState<string[]>([])
  const [editing, setEditing] = useState<InstanceDialogTarget | null>(null)
  const extension = useExtensionStatus()
  const extensionProviders = extension.status?.providers ?? []

  const labelOf = useCallback(
    (target: string) => selectionLabel(target, targets),
    [targets],
  )

  const loadActive = useCallback(async () => {
    try {
      const res = await getActiveModels()
      setActiveModels(Array.from(new Set(res.active_models || [])))
    } catch (cause) {
      toast.error(errorText(cause, t("models.inChat.loadFailed")))
    }
  }, [t])

  useEffect(() => {
    void loadActive()
  }, [loadActive])

  const reloadAll = async () => {
    await refresh()
    await loadActive()
  }

  // A sign-in, a sign-out or a token changes both what the extension
  // reports for the provider and the provider's instance.
  const reloadAfterSignIn = async () => {
    await Promise.all([extension.reload(), reloadAll()])
  }

  const mutate = async (operation: () => Promise<unknown>) => {
    try {
      await operation()
      await reloadAll()
      return true
    } catch (cause) {
      toast.error(errorText(cause, t("models.requestFailed")))
      return false
    }
  }

  const cards = useMemo(
    () => buildProviderCards(roster, instances),
    [roster, instances],
  )

  const instanceIds = useMemo(
    () => instances.map((instance) => instance.id),
    [instances],
  )

  const handlePing = async (instance: ProviderInstance) => {
    setPingingId(instance.id)
    try {
      const res = await pingProviderInstance(instance.id)
      setPingResults((prev) => ({ ...prev, [instance.id]: res }))
      if (res.ok) {
        toast.success(
          t("models.ping.toastOk", {
            ms: res.latency_ms,
            models: res.model_count ?? 0,
          }),
        )
      } else {
        toast.error(
          t("models.ping.toastFailed", {
            error: res.error || t("models.ping.unreachable"),
          }),
        )
      }
    } catch (cause) {
      toast.error(errorText(cause, t("models.requestFailed")))
    } finally {
      setPingingId(null)
    }
  }

  const handleSync = async (instance: ProviderInstance) => {
    setSyncingId(instance.id)
    try {
      const res = await syncProviderCatalog(instance.id)
      toast.success(
        t("models.sync.done", { count: res.total ?? res.models?.length ?? 0 }),
      )
      await reloadAll()
    } catch (cause) {
      toast.error(errorText(cause, t("models.requestFailed")))
    } finally {
      setSyncingId(null)
    }
  }

  const handleAutoConnectFree = async () => {
    setAutoConnecting(true)
    try {
      const res = await autoConnectFreeProviders()
      const stored: StoredFreeTest = {
        at: new Date().toISOString(),
        verified: res.verified,
        outcomes: res.outcomes ?? [],
      }
      saveFreeTest(stored)
      setFreeTest(stored)
      if (res.verified > 0) {
        toast.success(t("models.freeTest.toastAdded", { count: res.verified }))
      } else {
        toast.warning(t("models.freeTest.toastNone"))
      }
      // The first model added to Chat can become the default model, which
      // the default model bar shows after this refresh.
      await reloadAll()
    } catch (cause) {
      toast.error(errorText(cause, t("models.freeTest.failed")))
    } finally {
      setAutoConnecting(false)
    }
  }

  const handleToggleShortlist = async (target: string) => {
    const name = formatModelLabel(labelOf(target))
    if (activeModels.includes(target)) {
      if (await mutate(() => removeActiveModel(target)))
        toast.info(t("models.inChat.removed", { model: name }))
    } else if (await mutate(() => addActiveModel(target))) {
      toast.success(t("models.inChat.added", { model: name }))
    }
  }

  const handleSave = async (
    body: ProviderInstanceInput,
    existing: string | undefined,
    name: string,
  ) => {
    try {
      if (existing) {
        await updateProviderInstance(existing, body)
        toast.success(t("models.connect.saved", { name }))
      } else {
        await createProviderInstance(body)
        // A new connection is only useful with its models, so load them
        // right away rather than leaving a manual refresh to find.
        if (body.endpoint) {
          try {
            const synced = await syncProviderCatalog(body.id)
            toast.success(
              t("models.connect.connectedModels", {
                name,
                count: synced.total ?? synced.models?.length ?? 0,
              }),
            )
          } catch (cause) {
            toast.warning(
              t("models.connect.connectedNoModels", {
                name,
                error: errorText(cause, t("models.requestFailed")),
              }),
            )
          }
        } else {
          toast.success(t("models.connect.connected", { name }))
        }
      }
      await reloadAll()
      setEditing(null)
      if (!existing) {
        const card = cards.find(
          (item) => item.id === body.provider_kind || item.id === body.id,
        )
        setExpandedId(card?.id ?? body.id)
        if (card)
          setSelectedInstance((prev) => ({ ...prev, [card.id]: body.id }))
      }
    } catch (cause) {
      toast.error(errorText(cause, t("models.requestFailed")))
    }
  }

  const openConnect = (card: ProviderCard) =>
    setEditing({
      mode: "create",
      entry: card.roster,
      suggestedId: nextFreeId(card.roster?.id ?? card.id, instanceIds),
    })

  const query = search.trim().toLowerCase()
  const filteredCards = cards.filter((card) => {
    if (query) {
      const haystack = [
        card.id,
        card.name,
        card.description ?? "",
        card.roster?.protocol ?? "",
        ...card.instances.flatMap((instance) => [
          instance.id,
          instanceDisplayName(instance),
        ]),
      ]
        .join("\n")
        .toLowerCase()
      if (!haystack.includes(query)) return false
    }
    // Connected means usable now: a provider that still needs a sign-in, a
    // token or a key is not, however it was added.
    if (filter === "connected") return isCardConnected(card)
    if (filter === "free" || filter === "apikey" || filter === "local")
      return cardShelf(card) === filter
    return true
  })
  const shelves = SHELF_ORDER.map((key) => ({
    key,
    cards: filteredCards.filter((card) => cardShelf(card) === key),
  })).filter((shelf) => shelf.cards.length > 0)
  const connectedCount = cards.filter(isCardConnected).length

  const filters: { key: FilterKey; label: string }[] = [
    {
      key: "all",
      label: t("models.providers.filter.all", { total: cards.length }),
    },
    {
      key: "connected",
      label: t("models.providers.filter.connected", { total: connectedCount }),
    },
    { key: "free", label: t("models.providers.filter.free") },
    { key: "apikey", label: t("models.providers.filter.apikey") },
    { key: "local", label: t("models.providers.filter.local") },
  ]

  return (
    <div className="space-y-8">
      <ModelsInChat
        activeModels={activeModels}
        targets={targets}
        defaultModel={defaultModel}
        autoConnecting={autoConnecting}
        onTryFree={() => void handleAutoConnectFree()}
        onRemove={(target) => void handleToggleShortlist(target)}
      />

      {freeTest && (
        <FreeTestResults
          result={freeTest}
          roster={roster}
          onDismiss={() => {
            clearFreeTest()
            setFreeTest(null)
          }}
        />
      )}

      <section aria-labelledby="providers-heading" className="space-y-4">
        <div>
          <h3
            id="providers-heading"
            className="text-foreground text-xl font-bold tracking-tight"
          >
            {t("models.providers.title")}
          </h3>
          <p className="text-muted-foreground mt-0.5 text-sm">
            {t("models.providers.description")}
          </p>
        </div>

        <div className="flex flex-col gap-3">
          <div className="relative">
            <IconSearch className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2" />
            <Input
              type="search"
              value={search}
              onChange={(e) => {
                setSearch(e.target.value)
                setExpandedId("")
              }}
              placeholder={t("models.providers.search")}
              aria-label={t("models.providers.search")}
              className="pl-9"
            />
          </div>
          <div
            className="flex flex-wrap gap-1.5"
            role="group"
            aria-label={t("models.providers.filtersLabel")}
          >
            {filters.map((chip) => (
              <button
                key={chip.key}
                type="button"
                aria-pressed={filter === chip.key}
                onClick={() => {
                  setFilter(chip.key)
                  setExpandedId("")
                }}
                className={cn(
                  "rounded-full px-3 py-1 text-xs font-medium transition-colors",
                  filter === chip.key
                    ? "bg-primary text-primary-foreground shadow-xs"
                    : "bg-muted text-muted-foreground hover:bg-muted/80 hover:text-foreground",
                )}
              >
                {chip.label}
              </button>
            ))}
          </div>
        </div>

        <div className="flex flex-col gap-8 pt-2">
          {shelves.map((shelf) => {
            const expanded = shelf.cards.find((card) => card.id === expandedId)
            return (
              <ProviderShelf
                key={shelf.key}
                shelf={shelf.key}
                cards={shelf.cards}
                expandedId={expandedId}
                onCardClick={(card) => {
                  if (
                    card.instances.length === 0 &&
                    card.roster?.compatibility !== "discovery_only"
                  ) {
                    openConnect(card)
                    return
                  }
                  setExpandedId(expandedId === card.id ? "" : card.id)
                }}
              >
                {expanded && (
                  <CardInspector
                    card={expanded}
                    selectedId={selectedInstance[expanded.id]}
                    onSelectInstance={(id) =>
                      setSelectedInstance((prev) => ({
                        ...prev,
                        [expanded.id]: id,
                      }))
                    }
                    catalogs={catalogs}
                    targets={targets}
                    activeModels={activeModels}
                    pingResults={pingResults}
                    pingingId={pingingId}
                    syncingId={syncingId}
                    extensionProviders={extensionProviders}
                    extensionConnection={extension.status?.status}
                    onToggleShortlist={handleToggleShortlist}
                    onClose={() => setExpandedId("")}
                    onConnect={() => openConnect(expanded)}
                    onEdit={(instance) =>
                      setEditing({
                        mode: "edit",
                        instance,
                        entry: expanded.roster,
                      })
                    }
                    onPing={(instance) => void handlePing(instance)}
                    onSync={(instance) => void handleSync(instance)}
                    onDelete={setPendingDelete}
                    onSignInChanged={reloadAfterSignIn}
                  />
                )}
              </ProviderShelf>
            )
          })}

          {shelves.length === 0 && (
            <div className="rounded-xl border border-dashed p-12 text-center">
              <p className="text-foreground text-sm font-medium">
                {t("models.providers.noMatch")}
              </p>
              <p className="text-muted-foreground mt-1 text-xs">
                {t("models.providers.noMatchHint")}
              </p>
            </div>
          )}
        </div>
      </section>

      <ExtensionSection extension={extension} onChanged={reloadAll} />

      {editing && (
        <InstanceDialog
          target={editing}
          takenIds={instanceIds}
          onClose={() => setEditing(null)}
          onSave={handleSave}
        />
      )}
      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("models.remove.title", {
                name: pendingDelete ? instanceDisplayName(pendingDelete) : "",
              })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("models.remove.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                const instance = pendingDelete
                setPendingDelete(null)
                if (instance)
                  void mutate(() => deleteProviderInstance(instance.id))
              }}
            >
              {t("models.remove.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function ModelsInChat({
  activeModels,
  targets,
  defaultModel,
  autoConnecting,
  onTryFree,
  onRemove,
}: {
  activeModels: string[]
  targets: ProviderTarget[]
  defaultModel: DefaultModelState
  autoConnecting: boolean
  onTryFree: () => void
  onRemove: (target: string) => void
}) {
  const { t } = useTranslation()
  return (
    <section
      aria-labelledby="models-in-chat-heading"
      className="border-border bg-card rounded-xl border p-5 shadow-xs"
    >
      <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="flex items-center gap-2">
            <h3
              id="models-in-chat-heading"
              className="text-foreground text-base font-semibold"
            >
              {t("models.inChat.title")}
            </h3>
            <Badge
              variant="secondary"
              className="text-xs tabular-nums"
              aria-label={t("models.inChat.count", {
                count: activeModels.length,
              })}
            >
              {activeModels.length}
            </Badge>
          </div>
          <p className="text-muted-foreground mt-0.5 text-xs">
            {t("models.inChat.description")}
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          className="self-start sm:self-auto"
          disabled={autoConnecting}
          onClick={onTryFree}
        >
          <IconSparkles
            className={cn(
              "mr-1.5 size-4 text-amber-500",
              autoConnecting && "animate-spin",
            )}
          />
          {autoConnecting
            ? t("models.freeTest.running")
            : t("models.freeTest.run")}
        </Button>
      </div>

      {activeModels.length > 0 ? (
        <>
          {activeModels.length > 10 && (
            <p className="mb-2 text-xs text-amber-600 dark:text-amber-400">
              {t("models.inChat.crowded")}
            </p>
          )}
          <ul className="divide-border/50 border-border/80 bg-background/50 max-h-[420px] divide-y overflow-y-auto rounded-lg border">
            {activeModels.map((target) => {
              const label = selectionLabel(target, targets)
              return (
                <li
                  key={target}
                  className="flex items-center justify-between gap-3 p-3"
                >
                  <div
                    className="flex min-w-0 items-center gap-2.5"
                    title={target}
                  >
                    <ProviderIcon
                      provider={{ key: label.provider || target }}
                      className="size-5"
                    />
                    <div className="min-w-0">
                      <p className="text-foreground truncate text-sm font-medium">
                        {label.model}
                      </p>
                      {label.provider && (
                        <p className="text-muted-foreground truncate text-xs">
                          {label.provider}
                        </p>
                      )}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1 sm:gap-2">
                    <DefaultModelAction
                      selection={target}
                      name={formatModelLabel(label)}
                      defaultModel={defaultModel}
                    />
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:bg-destructive/10 h-7 px-2 text-xs"
                      aria-label={t("models.inChat.removeNamed", {
                        model: formatModelLabel(label),
                      })}
                      onClick={() => onRemove(target)}
                    >
                      <IconTrash className="size-3.5 sm:mr-1" />
                      <span className="hidden sm:inline">
                        {t("models.inChat.remove")}
                      </span>
                    </Button>
                  </div>
                </li>
              )
            })}
          </ul>
        </>
      ) : (
        <div className="border-border rounded-lg border border-dashed p-6 text-center">
          <p className="text-foreground text-sm font-medium">
            {t("models.inChat.empty")}
          </p>
          <p className="text-muted-foreground mx-auto mt-1 max-w-md text-xs">
            {t("models.inChat.emptyHint")}
          </p>
        </div>
      )}
    </section>
  )
}

function FreeTestResults({
  result,
  roster,
  onDismiss,
}: {
  result: StoredFreeTest
  roster: ProviderRosterEntry[]
  onDismiss: () => void
}) {
  const { t } = useTranslation()
  const nameOf = (registryID: string, providerID: string) =>
    roster.find((entry) => entry.id === registryID)?.display_name ||
    providerID ||
    registryID
  const statusText = (outcome: StoredFreeTest["outcomes"][number]) => {
    if (outcome.status === "verified")
      return outcome.latency_ms
        ? t("models.freeTest.status.verifiedLatency", {
            ms: outcome.latency_ms,
          })
        : t("models.freeTest.status.verified")
    // A reason this version does not know reads as the plain status.
    if (outcome.error_class && FREE_TEST_REASONS.has(outcome.error_class))
      return t(`models.freeTest.status.${outcome.error_class}`)
    // Connected: it listed its models, but its test answer failed.
    return outcome.status === "connected"
      ? t("models.freeTest.status.connected")
      : t("models.freeTest.status.failed")
  }
  return (
    <section
      aria-labelledby="free-test-heading"
      className="border-border/80 space-y-2 rounded-xl border p-4"
    >
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 id="free-test-heading" className="text-sm font-semibold">
            {t("models.freeTest.title")}
          </h3>
          <p className="text-muted-foreground text-xs">
            {t("models.freeTest.summary", {
              verified: result.verified,
              total: result.outcomes.length,
            })}{" "}
            ·{" "}
            {t("models.freeTest.testedAt", {
              when: dayjs(result.at).fromNow(),
            })}
          </p>
        </div>
        <Button
          size="icon"
          variant="ghost"
          className="size-7 shrink-0"
          aria-label={t("models.freeTest.dismiss")}
          title={t("models.freeTest.dismiss")}
          onClick={onDismiss}
        >
          <IconX className="size-4" />
        </Button>
      </div>
      <ul className="divide-border/60 divide-y rounded-lg border">
        {result.outcomes.map((outcome) => (
          <li
            key={`${outcome.registry_id}:${outcome.provider_id}`}
            className="flex flex-col gap-1 px-3 py-2 text-xs sm:flex-row sm:items-start sm:justify-between sm:gap-4"
          >
            <div className="min-w-0">
              <p className="font-medium">
                {nameOf(outcome.registry_id, outcome.provider_id)}
              </p>
              {outcome.probe_model && (
                <p className="text-muted-foreground mt-0.5 truncate">
                  {t("models.freeTest.probeModel", {
                    model: outcome.probe_model,
                  })}
                </p>
              )}
            </div>
            <div className="min-w-0 sm:max-w-[60%] sm:text-right">
              <p
                className={
                  outcome.status === "verified"
                    ? "text-emerald-600 dark:text-emerald-400"
                    : "text-amber-600 dark:text-amber-400"
                }
              >
                {statusText(outcome)}
              </p>
              {/* One short line; the whole text waits in its tooltip. */}
              {outcome.error && (
                <p
                  className="text-muted-foreground mt-0.5 line-clamp-2 break-words"
                  title={outcome.error}
                >
                  {outcome.error}
                </p>
              )}
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}

function ProviderShelf({
  shelf,
  cards,
  expandedId,
  onCardClick,
  children,
}: {
  shelf: ShelfKey
  cards: ProviderCard[]
  expandedId: string
  onCardClick: (card: ProviderCard) => void
  children?: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <section aria-labelledby={`shelf-${shelf}`} className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <div className="flex items-center gap-2">
          <h4
            id={`shelf-${shelf}`}
            className="text-foreground text-sm font-semibold tracking-wide"
          >
            {t(`models.providers.shelf.${shelf}.title`)}
          </h4>
          <span className="text-muted-foreground text-xs font-normal tabular-nums">
            {cards.length}
          </span>
        </div>
        <span className="text-muted-foreground text-xs">
          {t(`models.providers.shelf.${shelf}.description`)}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5">
        {cards.map((card) => {
          const configured = card.instances.length > 0
          const status = cardStatus(card)
          const isExpanded = expandedId === card.id
          const label = configured
            ? t("models.providers.manage", { name: card.name })
            : t("models.providers.connect", { name: card.name })
          return (
            <button
              key={card.id}
              type="button"
              onClick={() => onCardClick(card)}
              aria-expanded={configured ? isExpanded : undefined}
              aria-label={label}
              title={label}
              className={cn(
                "hover:bg-muted/50 flex min-w-0 items-center gap-2.5 rounded-lg border p-3 text-left transition-all",
                isExpanded
                  ? "border-primary bg-primary/5 ring-primary ring-1"
                  : "border-border bg-card",
              )}
            >
              <ProviderIcon
                provider={{
                  key: card.roster ? card.id : card.name,
                  label: card.name,
                }}
              />
              <span className="min-w-0 flex-1">
                <span className="text-foreground block truncate text-xs font-medium">
                  {card.name}
                </span>
                {status && (
                  <span
                    className={cn(
                      "block truncate text-[11px]",
                      STATUS_CLASS[status],
                    )}
                  >
                    {t(`models.status.${status}`)}
                  </span>
                )}
              </span>
            </button>
          )
        })}
      </div>
      {children}
    </section>
  )
}

/**
 * The details of a card and what can be done with it. A provider the
 * extension serves shows its own sign-in, and neither an address nor a way
 * to edit, test or remove it: the extension owns all of that.
 */
function CardInspector({
  card,
  selectedId,
  onSelectInstance,
  catalogs,
  targets,
  activeModels,
  pingResults,
  pingingId,
  syncingId,
  extensionProviders,
  extensionConnection,
  onToggleShortlist,
  onClose,
  onConnect,
  onEdit,
  onPing,
  onSync,
  onDelete,
  onSignInChanged,
}: {
  card: ProviderCard
  selectedId?: string
  onSelectInstance: (id: string) => void
  catalogs: ProviderInstanceCatalog[]
  targets: ProviderTarget[]
  activeModels: string[]
  pingResults: Record<string, PingResult>
  pingingId: string | null
  syncingId: string | null
  /** The providers the extension serves, each naming its instance. */
  extensionProviders: readonly ExtensionProvider[]
  /** The extension's connection; undefined until its status loads. */
  extensionConnection?: ExtensionConnectionStatus
  onToggleShortlist: (target: string) => Promise<void>
  onClose: () => void
  onConnect: () => void
  onEdit: (instance: ProviderInstance) => void
  onPing: (instance: ProviderInstance) => void
  onSync: (instance: ProviderInstance) => void
  onDelete: (instance: ProviderInstance) => void
  /** Called after a sign-in, a sign-out or a token change. */
  onSignInChanged: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const instance =
    card.instances.find((item) => item.id === selectedId) ?? card.instances[0]
  const catalog = instance
    ? catalogs.find((item) => item.instance_id === instance.id)
    : undefined
  const models = catalog?.models ?? []
  const status = instance ? instanceStatus(instance) : undefined
  const managed = instance ? isExtensionManaged(instance) : false
  const extensionProvider =
    managed && instance
      ? extensionProviders.find(
          (provider) => provider.instance_id === instance.id,
        )
      : undefined
  const pingResult = instance ? pingResults[instance.id] : undefined
  const entry = card.roster
  const credential = instance
    ? instanceCredentialKind(instance)
    : entry?.requires_api_key
      ? "api_key"
      : "none"
  const shelf = cardShelf(card)
  const protocol =
    instance?.protocol || entry?.protocol || entry?.adapter || "openai"
  const usedFor = instance && managed ? instanceSurfaces(instance, models) : []

  const refreshButton = instance && (
    <Button
      size="sm"
      variant="outline"
      disabled={syncingId === instance.id}
      onClick={() => onSync(instance)}
    >
      <IconRefresh
        className={cn(
          "mr-1 size-4",
          syncingId === instance.id && "animate-spin",
        )}
      />
      {t("models.actions.refresh")}
    </Button>
  )

  return (
    <div
      role="region"
      aria-label={card.name}
      className="border-border bg-card mt-3 rounded-xl border p-5 shadow-xs"
    >
      <div className="flex items-start justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          <ProviderIcon
            provider={{ key: entry ? card.id : card.name, label: card.name }}
            className="size-6"
          />
          <div className="min-w-0">
            <h4 className="text-foreground truncate text-base font-semibold">
              {card.name}
            </h4>
            {status && (
              <p className={cn("text-xs", STATUS_CLASS[status])}>
                {t(`models.status.${status}`)}
              </p>
            )}
          </div>
        </div>
        <Button
          size="icon"
          variant="ghost"
          className="size-7 shrink-0"
          onClick={onClose}
          aria-label={t("models.inspector.close")}
        >
          <IconX className="size-4" />
        </Button>
      </div>

      {card.instances.length > 1 && (
        <div
          className="mt-3 flex flex-wrap gap-1.5"
          role="group"
          aria-label={t("models.inspector.connections")}
        >
          {card.instances.map((item) => (
            <button
              key={item.id}
              type="button"
              aria-pressed={item.id === instance?.id}
              onClick={() => onSelectInstance(item.id)}
              className={cn(
                "rounded-full border px-2.5 py-0.5 text-xs transition-colors",
                item.id === instance?.id
                  ? "border-primary bg-primary/10 text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {item.id}
            </button>
          ))}
        </div>
      )}

      {card.description && (
        <p className="text-muted-foreground mt-3 max-w-3xl text-sm leading-relaxed">
          {card.description}
        </p>
      )}

      <div className="border-border/60 mt-4 space-y-3 border-y py-3 text-xs">
        <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div>
            <dt className="text-muted-foreground font-medium">
              {t("models.inspector.models")}
            </dt>
            <dd className="text-foreground mt-1 font-semibold">
              {models.length
                ? t("models.inspector.modelsCount", { models: models.length })
                : t("models.inspector.modelsNone")}
            </dd>
          </div>
          {managed ? (
            usedFor.length > 0 && (
              <div>
                <dt className="text-muted-foreground font-medium">
                  {t("models.inspector.usedFor")}
                </dt>
                <dd className="text-foreground mt-1 font-semibold">
                  {surfacesText(usedFor, t)}
                </dd>
              </div>
            )
          ) : (
            <div>
              <dt className="text-muted-foreground font-medium">
                {t("models.inspector.api")}
              </dt>
              <dd className="text-foreground mt-1 font-semibold">
                {isKnownSurface(protocol)
                  ? surfacesText([protocol], t)
                  : protocol}
              </dd>
            </div>
          )}
          <div>
            <dt className="text-muted-foreground font-medium">
              {t("models.inspector.credential")}
            </dt>
            <dd className="text-foreground mt-1 font-semibold">
              {t(`models.credential.${credential}`)}
            </dd>
          </div>
          {!managed && (
            <div>
              <dt className="text-muted-foreground font-medium">
                {t("models.inspector.type")}
              </dt>
              <dd className="text-foreground mt-1 font-semibold">
                {t(`models.providers.shelf.${shelf}.title`)}
              </dd>
            </div>
          )}
          {!managed && (
            <div className="col-span-2 sm:col-span-4">
              <dt className="text-muted-foreground font-medium">
                {t("models.inspector.address")}
              </dt>
              <dd className="text-muted-foreground mt-1 font-mono text-[11px] break-all">
                {instance?.endpoint ||
                  entry?.default_endpoint ||
                  t("models.inspector.addressNone")}
              </dd>
            </div>
          )}
        </dl>
        {managed && (
          <p className="text-muted-foreground">
            {t("models.inspector.servedByExtension")}
          </p>
        )}
      </div>

      {instance && (
        <InstanceModels
          key={instance.id}
          instance={instance}
          models={models}
          targets={targets}
          activeModels={activeModels}
          onToggleShortlist={onToggleShortlist}
        />
      )}

      <div className="mt-5 flex flex-wrap items-center gap-2">
        {!instance ? (
          entry?.compatibility === "discovery_only" ? (
            <p className="text-muted-foreground text-xs">
              {t("models.inspector.discoveryOnly")}
            </p>
          ) : (
            <Button size="sm" onClick={onConnect}>
              <IconPlus className="mr-1 size-4" />
              {t("models.actions.connect")}
            </Button>
          )
        ) : managed ? (
          <>
            {extensionProvider ? (
              <ExtensionSignIn
                provider={extensionProvider}
                onChanged={onSignInChanged}
              />
            ) : extensionConnection === "connected" ? (
              // The extension dropped it; it stays listed, but disabled.
              <p className="text-muted-foreground text-xs">
                {t("models.inspector.extensionDropped")}
              </p>
            ) : (
              // Without the extension its providers cannot be signed in to.
              extensionConnection !== undefined &&
              credential !== "none" && (
                <p className="text-muted-foreground text-xs">
                  {t("models.inspector.extensionNeeded", {
                    section: t("models.extension.title"),
                  })}
                </p>
              )
            )}
            {/* Its models only load once it is ready. */}
            {status === "connected" && refreshButton}
          </>
        ) : (
          <>
            {entry && (
              <Button size="sm" variant="outline" onClick={onConnect}>
                <IconPlus className="mr-1 size-4" />
                {t("models.actions.addAnother")}
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={pingingId === instance.id}
              onClick={() => onPing(instance)}
            >
              <IconPlug className="mr-1 size-4" />
              {pingingId === instance.id
                ? t("models.actions.testing")
                : t("models.actions.test")}
            </Button>
            {refreshButton}
            <Button
              size="sm"
              variant="outline"
              onClick={() => onEdit(instance)}
            >
              {t("models.management.actions.edit")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-destructive hover:bg-destructive/10"
              onClick={() => onDelete(instance)}
            >
              <IconTrash className="mr-1 size-4" />
              {t("models.actions.remove")}
            </Button>
            {pingResult && (
              <span
                className={cn(
                  "rounded-md px-2.5 py-1 text-xs font-medium",
                  pingResult.ok
                    ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                    : "bg-destructive/10 text-destructive",
                )}
              >
                {pingResult.ok
                  ? t("models.ping.ok", {
                      ms: pingResult.latency_ms,
                      models: pingResult.model_count ?? 0,
                    })
                  : t("models.ping.failed", {
                      error: pingResult.error || t("models.ping.unreachable"),
                    })}
              </span>
            )}
          </>
        )}
      </div>
    </div>
  )
}

/**
 * The sign-in a provider the extension serves offers on its card, or why it
 * offers none: it needs no key but its models did not load, or Compa cannot
 * sign in to it.
 */
function ExtensionSignIn({
  provider,
  onChanged,
}: {
  provider: ExtensionProvider
  onChanged: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const state = extensionProviderState(provider)
  if (state === "unsupported")
    return (
      <p className="text-muted-foreground text-xs">
        {provider.reason || t("models.extension.state.unsupported")}
      </p>
    )
  if (state === "not_ready")
    return (
      <p className="text-xs text-amber-600 dark:text-amber-400">
        {t("models.extension.state.not_ready")}
      </p>
    )
  return <ExtensionProviderActions provider={provider} onChanged={onChanged} />
}
