import { type ReactNode, useId, useState } from "react"
import { useTranslation } from "react-i18next"

import { THINKING_LEVELS, type ThinkingLevel } from "@/api/provider-instances"
import {
  AdvancedSection,
  Field,
  SwitchCardField,
} from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"

import {
  type RuntimeErrorCode,
  type RuntimeFormErrors,
  type RuntimeFormState,
  buildRuntimeSettings,
  streamingOn,
} from "./runtime-settings"

// Radix Select items cannot carry an empty value.
const DEFAULT_THINKING = "__default__"

interface RuntimeSettingsEditorProps {
  value: RuntimeFormState
  onChange: (value: RuntimeFormState) => void
  /** Errors to show; the section stays open while there are any. */
  errors?: RuntimeFormErrors
  /** More advanced fields of the dialog, shown above the request settings. */
  children?: ReactNode
}

/**
 * The collapsible advanced section of a provider instance dialog that edits
 * the request settings every model on the instance runs with.
 */
export function RuntimeSettingsEditor({
  value,
  onChange,
  errors = {},
  children,
}: RuntimeSettingsEditorProps) {
  const { t } = useTranslation()
  const id = useId()
  const [open, setOpen] = useState(false)
  const hasErrors = Object.keys(errors).length > 0
  const built = buildRuntimeSettings(value)
  const configured = built.ok ? Object.keys(built.runtime).length : 0

  const set = <K extends keyof RuntimeFormState>(
    key: K,
    next: RuntimeFormState[K],
  ) => onChange({ ...value, [key]: next })

  const errorText: Record<RuntimeErrorCode, string> = {
    invalidProxy: t("models.management.runtime.errors.invalidProxy"),
    nonNegativeInteger: t(
      "models.management.runtime.errors.nonNegativeInteger",
    ),
    fieldName: t("models.management.runtime.errors.fieldName"),
    invalidJson: t("models.management.runtime.errors.invalidJson"),
    jsonObject: t("models.management.runtime.errors.jsonObject"),
    emptyKey: t("models.management.runtime.errors.emptyKey"),
  }
  const errorFor = (field: keyof RuntimeFormErrors) => {
    const code = errors[field]
    return code ? errorText[code] : undefined
  }

  const thinkingLabels: Record<ThinkingLevel, string> = {
    off: t("models.management.runtime.thinking.off"),
    low: t("models.management.runtime.thinking.low"),
    medium: t("models.management.runtime.thinking.medium"),
    high: t("models.management.runtime.thinking.high"),
    xhigh: t("models.management.runtime.thinking.xhigh"),
    adaptive: t("models.management.runtime.thinking.adaptive"),
  }

  return (
    <AdvancedSection
      open={open || hasErrors}
      onOpenChange={setOpen}
      summary={
        configured > 0 && (
          <Badge variant="secondary">
            {t("models.management.runtime.configured", { count: configured })}
          </Badge>
        )
      }
    >
      {children}
      <p className="text-muted-foreground text-xs leading-normal">
        {t("models.management.runtime.description")}
      </p>

      <div className="grid gap-5 sm:grid-cols-2">
        <Field
          label={t("models.management.runtime.thinkingLevel")}
          hint={t("models.management.runtime.thinkingLevelHint")}
          htmlFor={`${id}-thinking`}
        >
          <Select
            value={value.thinkingLevel || DEFAULT_THINKING}
            onValueChange={(next) =>
              set(
                "thinkingLevel",
                next === DEFAULT_THINKING ? "" : (next as ThinkingLevel),
              )
            }
          >
            <SelectTrigger id={`${id}-thinking`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_THINKING}>
                {t("models.management.runtime.thinking.default")}
              </SelectItem>
              {THINKING_LEVELS.map((level) => (
                <SelectItem key={level} value={level}>
                  {thinkingLabels[level]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t("models.management.runtime.proxy")}
          hint={t("models.management.runtime.proxyHint")}
          error={errorFor("proxy")}
          htmlFor={`${id}-proxy`}
        >
          <Input
            id={`${id}-proxy`}
            value={value.proxy}
            onChange={(event) => set("proxy", event.target.value)}
            placeholder="http://127.0.0.1:7890"
            aria-invalid={Boolean(errors.proxy)}
            spellCheck={false}
          />
        </Field>

        <Field
          label={t("models.management.runtime.requestTimeout")}
          hint={t("models.management.runtime.requestTimeoutHint")}
          error={errorFor("requestTimeout")}
          htmlFor={`${id}-timeout`}
        >
          <Input
            id={`${id}-timeout`}
            type="number"
            inputMode="numeric"
            min={0}
            step={1}
            value={value.requestTimeout}
            onChange={(event) => set("requestTimeout", event.target.value)}
            placeholder={t("models.management.runtime.providerDefault")}
            aria-invalid={Boolean(errors.requestTimeout)}
          />
        </Field>

        <Field
          label={t("models.management.runtime.rpm")}
          hint={t("models.management.runtime.rpmHint")}
          error={errorFor("rpm")}
          htmlFor={`${id}-rpm`}
        >
          <Input
            id={`${id}-rpm`}
            type="number"
            inputMode="numeric"
            min={0}
            step={1}
            value={value.rpm}
            onChange={(event) => set("rpm", event.target.value)}
            placeholder={t("models.management.runtime.unlimited")}
            aria-invalid={Boolean(errors.rpm)}
          />
        </Field>

        <Field
          label={t("models.management.runtime.maxTokensField")}
          hint={t("models.management.runtime.maxTokensFieldHint")}
          error={errorFor("maxTokensField")}
          htmlFor={`${id}-max-tokens`}
        >
          <Input
            id={`${id}-max-tokens`}
            value={value.maxTokensField}
            onChange={(event) => set("maxTokensField", event.target.value)}
            placeholder="max_completion_tokens"
            aria-invalid={Boolean(errors.maxTokensField)}
            spellCheck={false}
          />
        </Field>

        <Field
          label={t("models.management.runtime.toolSchemaTransform")}
          hint={t("models.management.runtime.toolSchemaTransformHint")}
          htmlFor={`${id}-tool-schema`}
        >
          <Input
            id={`${id}-tool-schema`}
            value={value.toolSchemaTransform}
            onChange={(event) => set("toolSchemaTransform", event.target.value)}
            placeholder="simple"
            list={`${id}-tool-schema-options`}
            spellCheck={false}
          />
          <datalist id={`${id}-tool-schema-options`}>
            <option value="off" />
            <option value="simple" />
          </datalist>
        </Field>
      </div>

      {/* Unset reads as on, as the server runs it; switching it saves the
          choice, and off is the only way to turn it off. */}
      <SwitchCardField
        label={t("models.management.runtime.streaming")}
        hint={t("models.management.runtime.streamingHint")}
        checked={streamingOn(value)}
        onCheckedChange={(checked) => set("streaming", checked)}
      />

      <Field
        label={t("models.management.runtime.extraBody")}
        hint={t("models.management.runtime.extraBodyHint")}
        error={errorFor("extraBody")}
        htmlFor={`${id}-extra-body`}
      >
        <Textarea
          id={`${id}-extra-body`}
          value={value.extraBody}
          onChange={(event) => set("extraBody", event.target.value)}
          placeholder='{"reasoning_split": true}'
          rows={4}
          className="font-mono text-xs"
          aria-invalid={Boolean(errors.extraBody)}
          spellCheck={false}
        />
      </Field>
    </AdvancedSection>
  )
}
