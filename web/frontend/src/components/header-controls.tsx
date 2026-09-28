import { IconLanguage, IconMoon, IconSun } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useTheme } from "@/hooks/use-theme"
import { UI_LANGUAGES, matchUILanguage } from "@/i18n/languages"
import { cn } from "@/lib/utils"

interface HeaderControlProps {
  className?: string
  variant?: "ghost" | "outline"
}

/** The interface language picker; its button is named for screen readers. */
export function LanguageMenu({
  className,
  variant = "ghost",
}: HeaderControlProps) {
  const { i18n, t } = useTranslation()
  const current = matchUILanguage(i18n.resolvedLanguage ?? i18n.language)
  const label = t("header.language.label")

  return (
    <DropdownMenu>
      <Tooltip delayDuration={500}>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button
              variant={variant}
              size="icon"
              className={cn("size-8", className)}
              aria-label={label}
            >
              <IconLanguage className="size-4.5" />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuRadioGroup
          value={current}
          onValueChange={(code) => void i18n.changeLanguage(code)}
        >
          {UI_LANGUAGES.map((language) => (
            <DropdownMenuRadioItem
              key={language.code}
              value={language.code}
              lang={language.code}
            >
              {language.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Switches between the light and the dark theme. */
export function ThemeToggle({ className, variant = "ghost" }: HeaderControlProps) {
  const { t } = useTranslation()
  const { theme, toggleTheme } = useTheme()
  const label =
    theme === "dark" ? t("header.theme.toLight") : t("header.theme.toDark")

  return (
    <Tooltip delayDuration={500}>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant={variant}
          size="icon"
          className={cn("size-8", className)}
          onClick={toggleTheme}
          aria-label={label}
        >
          {theme === "dark" ? (
            <IconSun className="size-4.5" />
          ) : (
            <IconMoon className="size-4.5" />
          )}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}
