import { useCallback, useEffect, useState } from "react"

import { readStoredValue, writeStoredValue } from "@/lib/storage"

type Theme = "light" | "dark"

const THEME_STORAGE_KEY = "theme"

function isTheme(value: unknown): value is Theme {
  return value === "light" || value === "dark"
}

// Mirrors the inline script in index.html, which applies the same choice
// before the first paint: a saved theme wins, otherwise the system's.
function getInitialTheme(): Theme {
  const stored = readStoredValue(THEME_STORAGE_KEY)
  if (isTheme(stored)) return stored
  if (typeof window === "undefined" || typeof window.matchMedia !== "function")
    return "dark"
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light"
}

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(getInitialTheme)

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark")
  }, [theme])

  // Only an explicit choice is saved, so an unset theme keeps following the
  // system.
  const toggleTheme = useCallback(() => {
    const next: Theme = theme === "dark" ? "light" : "dark"
    writeStoredValue(THEME_STORAGE_KEY, next)
    setThemeState(next)
  }, [theme])

  return { theme, toggleTheme }
}
