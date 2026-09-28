import { useState } from "react"
import { useTranslation } from "react-i18next"

import type {
  ProviderInstance,
  ProviderInstanceInput,
  ProviderRosterEntry,
} from "@/api/provider-instances"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

import { ProviderIcon } from "./provider-icon"
import {
  instanceDisplayName,
  isExtensionManaged,
  takesAPIKey,
} from "./provider-model"
import {
  buildRuntimeSettings,
  runtimeFormFromSettings,
} from "./runtime-settings"
import { RuntimeSettingsEditor } from "./runtime-settings-editor"

/** What the dialog connects: a new instance of a provider, or an edit. */
export type InstanceDialogTarget =
  | {
      mode: "create"
      entry?: ProviderRosterEntry
      /** A free instance id to start from. */
      suggestedId: string
    }
  | { mode: "edit"; instance: ProviderInstance; entry?: ProviderRosterEntry }

interface InstanceDialogProps {
  target: InstanceDialogTarget
  /** Instance ids already in use, which a new instance cannot take. */
  takenIds: readonly string[]
  onClose: () => void
  onSave: (
    body: ProviderInstanceInput,
    existing: string | undefined,
    name: string,
  ) => Promise<void>
}

interface FieldErrors {
  id?: string
  endpoint?: string
  apiKey?: string
}

/**
 * Connects a provider — usually by pasting its API key — or edits a
 * connection. Settings few people need wait under the advanced section.
 */
export function InstanceDialog({
  target,
  takenIds,
  onClose,
  onSave,
}: InstanceDialogProps) {
  const { t } = useTranslation()
  const existing = target.mode === "edit" ? target.instance : null
  const entry = target.entry
  const managed = existing ? isExtensionManaged(existing) : false
  const name = existing
    ? instanceDisplayName(existing)
    : entry?.display_name || t("models.connect.customName")
  const [id, setID] = useState(
    existing?.id ?? (target.mode === "create" ? target.suggestedId : ""),
  )
  const providerKind = existing?.provider_kind || entry?.id || ""
  const adapter = existing?.adapter || entry?.adapter || "openai-compatible"
  const protocol = existing?.protocol || entry?.protocol || "openai"
  const [endpoint, setEndpoint] = useState(
    existing?.endpoint || entry?.default_endpoint || "",
  )
  const state: "enabled" | "disabled" = existing?.state || "enabled"
  const [authRef, setAuthRef] = useState("")
  const [apiKey, setAPIKey] = useState("")
  const [runtimeForm, setRuntimeForm] = useState(() =>
    runtimeFormFromSettings(existing?.runtime),
  )
  // Errors show once a save was attempted, then follow the edits.
  const [attempted, setAttempted] = useState(false)
  const [saving, setSaving] = useState(false)
  const runtime = buildRuntimeSettings(runtimeForm)
  const runtimeErrors = attempted && !runtime.ok ? runtime.errors : undefined

  const asksForKey = !managed && (existing ? true : takesAPIKey(entry))
  const keyRequired = !existing && Boolean(entry?.requires_api_key)
  const addressRequired =
    !managed && (Boolean(entry?.requires_base_url) || !entry?.default_endpoint)

  const fieldErrors = (): FieldErrors => {
    const errors: FieldErrors = {}
    if (!existing) {
      if (!id.trim()) errors.id = t("models.connect.errors.name")
      else if (takenIds.includes(id.trim()))
        errors.id = t("models.connect.errors.nameTaken")
    }
    if (addressRequired && !endpoint.trim())
      errors.endpoint = t("models.connect.errors.address")
    if (keyRequired && !apiKey.trim())
      errors.apiKey = t("models.connect.errors.apiKey")
    return errors
  }
  const errors = attempted ? fieldErrors() : {}

  const submit = async () => {
    setAttempted(true)
    if (!runtime.ok || Object.keys(fieldErrors()).length > 0) return
    const body: ProviderInstanceInput = {
      id: id.trim(),
      provider_kind: providerKind,
      adapter,
      protocol,
      endpoint: endpoint.trim(),
      state,
      runtime: runtime.runtime,
    }
    const writeOnly: NonNullable<ProviderInstanceInput["write_only"]> = {}
    if (authRef.trim()) writeOnly.auth_connection_ref = authRef.trim()
    if (apiKey.trim()) writeOnly.api_key = apiKey.trim()
    if (Object.keys(writeOnly).length > 0) body.write_only = writeOnly
    setSaving(true)
    try {
      await onSave(body, existing?.id, name)
    } finally {
      setSaving(false)
    }
  }

  const description = managed
    ? t("models.connect.descriptionManaged")
    : existing
      ? t("models.connect.descriptionEdit")
      : entry?.description ||
        (asksForKey
          ? t("models.connect.description")
          : t("models.connect.descriptionKeyless"))

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
        <form
          noValidate
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <div className="mb-1 flex items-center gap-2">
              <ProviderIcon
                provider={{ key: providerKind || id, label: name }}
                className="size-5"
              />
              <DialogTitle>
                {existing
                  ? t("models.connect.editTitle", { name })
                  : t("models.connect.title", { name })}
              </DialogTitle>
            </div>
            <DialogDescription className="text-xs">
              {description}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 text-xs">
            {asksForKey && (
              <div className="space-y-1.5">
                <Label htmlFor="instance-api-key">
                  {keyRequired || existing
                    ? t("models.connect.apiKey")
                    : t("models.connect.apiKeyOptional")}
                </Label>
                <Input
                  id="instance-api-key"
                  type="password"
                  autoComplete="off"
                  value={apiKey}
                  onChange={(e) => setAPIKey(e.target.value)}
                  placeholder={
                    existing
                      ? t("models.connect.apiKeyKeep")
                      : t("models.connect.apiKeyPlaceholder")
                  }
                  aria-invalid={errors.apiKey ? true : undefined}
                />
                {errors.apiKey && (
                  <p className="text-destructive">{errors.apiKey}</p>
                )}
              </div>
            )}

            {!managed && (
              <div className="space-y-1.5">
                <Label htmlFor="instance-endpoint">
                  {t("models.connect.address")}
                </Label>
                <Input
                  id="instance-endpoint"
                  value={endpoint}
                  onChange={(e) => setEndpoint(e.target.value)}
                  placeholder="https://api.example.com/v1"
                  spellCheck={false}
                  aria-invalid={errors.endpoint ? true : undefined}
                />
                <p className="text-muted-foreground">
                  {t("models.connect.addressHint")}
                </p>
                {errors.endpoint && (
                  <p className="text-destructive">{errors.endpoint}</p>
                )}
              </div>
            )}

            <div className="space-y-1.5">
              <Label htmlFor="instance-id">{t("models.connect.nameLabel")}</Label>
              <Input
                id="instance-id"
                value={id}
                disabled={Boolean(existing)}
                onChange={(e) => setID(e.target.value)}
                placeholder={t("models.connect.namePlaceholder")}
                spellCheck={false}
                aria-invalid={errors.id ? true : undefined}
              />
              {!existing && (
                <p className="text-muted-foreground">
                  {t("models.connect.nameHint")}
                </p>
              )}
              {errors.id && <p className="text-destructive">{errors.id}</p>}
            </div>

            <RuntimeSettingsEditor
              value={runtimeForm}
              onChange={setRuntimeForm}
              errors={runtimeErrors}
            >
              {!managed && (
                <div className="space-y-1.5">
                  <Label htmlFor="instance-credential-ref">
                    {t("models.connect.credentialRef")}
                  </Label>
                  <Input
                    id="instance-credential-ref"
                    type="password"
                    autoComplete="off"
                    value={authRef}
                    onChange={(e) => setAuthRef(e.target.value)}
                    placeholder={
                      existing
                        ? t("models.connect.credentialRefKeep")
                        : "credential:provider-name"
                    }
                  />
                  <p className="text-muted-foreground">
                    {t("models.connect.credentialRefHint")}
                  </p>
                </div>
              )}
            </RuntimeSettingsEditor>
            {runtimeErrors && (
              <p role="alert" className="text-destructive text-xs">
                {t("models.management.runtime.errors.summary")}
              </p>
            )}
          </div>

          <DialogFooter className="gap-2 sm:gap-0">
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={saving}>
              {existing ? t("models.connect.save") : t("models.connect.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
