import {
  IconArrowDown,
  IconArrowUp,
  IconRoute,
  IconTrash,
} from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ModelRoute,
  type ProviderTarget,
  createModelRoute,
  deleteModelRoute,
  servesChat,
  updateModelRoute,
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
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import type { DefaultModelState } from "@/hooks/use-default-model"
import {
  formatModelLabel,
  selectionLabel,
  targetInstanceLabel,
  targetModelLabel,
} from "@/lib/model-labels"

import { DefaultModelAction } from "./default-model"

interface RoutesPanelProps {
  targets: ProviderTarget[]
  routes: ModelRoute[]
  refresh: () => Promise<void>
  defaultModel: DefaultModelState
}

/** The targets of one instance, under the instance's name. */
interface TargetGroup {
  instanceId: string
  label: string
  targets: ProviderTarget[]
}

/** Targets grouped by the instance serving them, in the order they come. */
function groupByInstance(targets: readonly ProviderTarget[]): TargetGroup[] {
  const groups = new Map<string, TargetGroup>()
  for (const target of targets) {
    const group = groups.get(target.instance_id)
    if (group) group.targets.push(target)
    else
      groups.set(target.instance_id, {
        instanceId: target.instance_id,
        label: targetInstanceLabel(target),
        targets: [target],
      })
  }
  return [...groups.values()]
}

/** A target as a model name with its exact selection beneath it. */
function TargetName({
  target,
  targets,
}: {
  target: string
  targets: readonly ProviderTarget[]
}) {
  const label = selectionLabel(target, targets)
  return (
    <span className="min-w-0">
      <span className="text-foreground block truncate text-sm">
        {formatModelLabel(label)}
      </span>
      <span className="text-muted-foreground block truncate font-mono text-[11px]">
        {target}
      </span>
    </span>
  )
}

export function RoutesPanel({
  targets,
  routes,
  refresh,
  defaultModel,
}: RoutesPanelProps) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<ModelRoute | null>(null)
  const [creating, setCreating] = useState(false)
  const [pendingDelete, setPendingDelete] = useState("")
  // A route answers chats, so only models that serve chat are listed, as
  // they are in the route dialog: a speech provider's voices are not.
  const chatTargets = targets.filter((target) => servesChat(target.surfaces))
  const groups = groupByInstance(chatTargets)
  const mutate = async (operation: () => Promise<unknown>) => {
    try {
      await operation()
      await refresh()
      return true
    } catch (cause) {
      toast.error(
        cause instanceof Error && cause.message
          ? cause.message
          : t("models.requestFailed"),
      )
      return false
    }
  }
  return (
    <>
      <div className="mb-6 flex flex-col items-start justify-between gap-4 sm:flex-row">
        <div>
          <h2 className="text-lg font-semibold">
            {t("models.management.routes.title")}
          </h2>
          <p className="text-muted-foreground mt-1 max-w-2xl text-sm">
            {t("models.management.routes.description")}
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          <IconRoute className="size-4" />
          {t("models.management.routes.add")}
        </Button>
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <div>
          <h3 className="mb-3 font-medium">
            {t("models.management.routes.listTitle")}
          </h3>
          <div className="divide-border overflow-hidden rounded-xl border">
            {routes.length === 0 && (
              <p className="text-muted-foreground p-4 text-sm">
                {t("models.management.routes.empty")}
              </p>
            )}
            {routes.map((route) => (
              <div
                key={route.name}
                className="flex items-center justify-between gap-3 p-4"
              >
                <div className="min-w-0">
                  <strong className="block truncate">{route.name}</strong>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {t("models.management.routes.orderedTargets", {
                      count: route.targets.length,
                    })}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <DefaultModelAction
                    selection={route.name}
                    defaultModel={defaultModel}
                  />
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setEditing(route)}
                  >
                    {t("models.management.actions.edit")}
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label={t("models.management.actions.deleteRoute", {
                      name: route.name,
                    })}
                    onClick={() => setPendingDelete(route.name)}
                  >
                    <IconTrash className="size-4" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </div>
        <div>
          <h3 className="mb-3 font-medium">
            {t("models.management.routes.verifiedTitle")}
          </h3>
          <div className="max-h-96 overflow-y-auto rounded-xl border">
            {groups.length === 0 && (
              <p className="text-muted-foreground p-4 text-sm">
                {t("models.management.routes.noModels", {
                  tab: t("models.management.tabs.providers"),
                })}
              </p>
            )}
            {groups.map((group) => (
              <div
                key={group.instanceId}
                role="group"
                aria-label={group.label}
                className="border-border/60 border-b last:border-b-0"
              >
                <h4 className="bg-muted/40 flex items-baseline justify-between gap-3 px-3 py-2 text-xs">
                  <span className="text-foreground truncate font-semibold">
                    {group.label}
                  </span>
                  <span className="text-muted-foreground shrink-0 tabular-nums">
                    {t("models.management.routes.groupCount", {
                      count: group.targets.length,
                    })}
                  </span>
                </h4>
                <ul className="divide-border/60 divide-y">
                  {group.targets.map((target) => (
                    <li
                      key={target.target}
                      className="min-w-0 px-3 py-2"
                      title={target.target}
                    >
                      <span className="text-foreground block truncate text-sm">
                        {targetModelLabel(target)}
                      </span>
                      <span className="text-muted-foreground block truncate font-mono text-[11px]">
                        {target.target}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>
        </div>
      </div>
      {(creating || editing) && (
        <RouteDialog
          route={editing}
          targets={chatTargets}
          onClose={() => {
            setEditing(null)
            setCreating(false)
          }}
          onSave={async (saved) => {
            const ok = await mutate(() =>
              editing
                ? updateModelRoute(editing.name, saved)
                : createModelRoute(saved),
            )
            if (ok) {
              setEditing(null)
              setCreating(false)
            }
          }}
        />
      )}
      <AlertDialog
        open={pendingDelete !== ""}
        onOpenChange={(open) => {
          if (!open) setPendingDelete("")
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("models.management.routes.deleteTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("models.management.routes.deleteDescription", {
                name: pendingDelete,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                const name = pendingDelete
                setPendingDelete("")
                void mutate(() => deleteModelRoute(name))
              }}
            >
              {t("models.management.routes.deleteConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function RouteDialog({
  route,
  targets,
  onClose,
  onSave,
}: {
  route: ModelRoute | null
  targets: ProviderTarget[]
  onClose: () => void
  onSave: (route: ModelRoute) => Promise<void>
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(route?.name || "")
  const [selected, setSelected] = useState<string[]>(route?.targets || [])
  const [candidate, setCandidate] = useState(targets[0]?.target || "")
  const available = targets.filter(
    (target) => !selected.includes(target.target),
  )

  const move = (index: number, delta: number) => {
    const next = [...selected]
    const item = next[index]
    next[index] = next[index + delta]
    next[index + delta] = item
    setSelected(next)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {route
              ? t("models.management.routes.editTitle")
              : t("models.management.routes.createTitle")}
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <Input
            value={name}
            disabled={Boolean(route)}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("models.management.routes.namePlaceholder")}
            aria-label={t("models.management.routes.nameLabel")}
          />
          <ol className="space-y-2">
            {selected.map((item, index) => (
              <li
                key={item}
                className="flex items-center justify-between gap-2 rounded-lg border p-2 text-sm"
              >
                <TargetName target={item} targets={targets} />
                <div className="flex shrink-0 gap-1">
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <IconArrowUp className="size-3" />
                    {t("models.management.routes.up")}
                  </Button>
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={index === selected.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <IconArrowDown className="size-3" />
                    {t("models.management.routes.down")}
                  </Button>
                  <Button
                    size="xs"
                    variant="ghost"
                    aria-label={t("models.management.routes.removeTarget", {
                      target: item,
                    })}
                    onClick={() =>
                      setSelected(selected.filter((entry) => entry !== item))
                    }
                  >
                    <IconTrash className="size-3" />
                  </Button>
                </div>
              </li>
            ))}
          </ol>
          {available.length > 0 && (
            <div className="flex gap-2">
              <select
                className="border-input bg-background min-w-0 flex-1 rounded-md border px-3 py-1 text-sm"
                value={candidate}
                aria-label={t("models.management.routes.targetLabel")}
                onChange={(e) => setCandidate(e.target.value)}
              >
                {available.map((item) => (
                  <option key={item.target} value={item.target}>
                    {formatModelLabel(selectionLabel(item.target, targets))}
                  </option>
                ))}
              </select>
              <Button
                size="sm"
                onClick={() => {
                  const next = available.some(
                    (item) => item.target === candidate,
                  )
                    ? candidate
                    : available[0].target
                  setSelected([...selected, next])
                  const remaining = available.filter(
                    (item) => item.target !== next,
                  )
                  setCandidate(remaining[0]?.target || "")
                }}
              >
                {t("models.management.routes.addTarget")}
              </Button>
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => onSave({ name, targets: selected })}
            disabled={!name.trim() || selected.length === 0}
          >
            {t("models.management.routes.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
