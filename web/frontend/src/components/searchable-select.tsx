import { IconCheck, IconChevronDown, IconSearch } from "@tabler/icons-react"
import {
  type KeyboardEvent,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react"

import { cn } from "@/lib/utils"

export interface SearchableSelectOption {
  value: string
  label: string
  /** Secondary text, e.g. the provider serving a model. */
  description?: string
  /** More text the search matches, e.g. the exact target. */
  keywords?: string
}

export interface SearchableSelectGroup {
  key: string
  label?: string
  options: SearchableSelectOption[]
}

interface SearchableSelectProps {
  /** The accessible name of the control. */
  label: string
  value: string
  onValueChange: (value: string) => void
  groups: SearchableSelectGroup[]
  /** Options listed above the groups and never filtered out, e.g. "None". */
  leading?: SearchableSelectOption[]
  /** Options listed below the groups and never filtered out. */
  trailing?: SearchableSelectOption[]
  /** The trigger's text when no option holds the value. */
  placeholder: string
  searchPlaceholder: string
  emptyText: string
  id?: string
  disabled?: boolean
}

const matches = (option: SearchableSelectOption, query: string, group = "") =>
  [option.label, option.description, option.keywords, option.value, group]
    .filter(Boolean)
    .join("\n")
    .toLowerCase()
    .includes(query)

/**
 * A select for long lists: typing filters the options, which stay grouped.
 * Arrow keys move through what is shown and Enter picks it.
 *
 * The list opens in place below the control rather than floating over the
 * page, so it is never clipped by a card and needs no positioning.
 */
export function SearchableSelect({
  label,
  value,
  onValueChange,
  groups,
  leading = [],
  trailing = [],
  placeholder,
  searchPlaceholder,
  emptyText,
  id,
  disabled = false,
}: SearchableSelectProps) {
  const autoId = useId()
  const listId = `${id ?? autoId}-list`
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)

  const normalized = query.trim().toLowerCase()
  const visibleGroups = useMemo(
    () =>
      groups
        .map((group) => ({
          ...group,
          options: normalized
            ? group.options.filter((option) =>
                matches(option, normalized, group.label),
              )
            : group.options,
        }))
        .filter((group) => group.options.length > 0),
    [groups, normalized],
  )
  const flat = useMemo(
    () => [
      ...leading,
      ...visibleGroups.flatMap((group) => group.options),
      ...trailing,
    ],
    [leading, visibleGroups, trailing],
  )
  const positions = useMemo(
    () => new Map(flat.map((option, position) => [option.value, position])),
    [flat],
  )
  const optionId = (position: number) => `${listId}-${position}`
  const all = [...leading, ...groups.flatMap((g) => g.options), ...trailing]
  const selected = all.find((option) => option.value === value)

  useEffect(() => {
    if (!open) return
    document
      .getElementById(`${listId}-${active}`)
      ?.scrollIntoView?.({ block: "nearest" })
  }, [active, open, listId])

  // A press anywhere else closes the list.
  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node | null))
        setOpen(false)
    }
    document.addEventListener("pointerdown", onPointerDown)
    return () => document.removeEventListener("pointerdown", onPointerDown)
  }, [open])

  const show = () => {
    setQuery("")
    const index = all.findIndex((option) => option.value === value)
    setActive(index >= 0 ? index : 0)
    setOpen(true)
  }

  const close = () => {
    setOpen(false)
    triggerRef.current?.focus()
  }

  const choose = (option: SearchableSelectOption) => {
    onValueChange(option.value)
    close()
  }

  const onSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown") {
      event.preventDefault()
      setActive((index) => Math.min(index + 1, flat.length - 1))
    } else if (event.key === "ArrowUp") {
      event.preventDefault()
      setActive((index) => Math.max(index - 1, 0))
    } else if (event.key === "Home") {
      event.preventDefault()
      setActive(0)
    } else if (event.key === "End") {
      event.preventDefault()
      setActive(Math.max(flat.length - 1, 0))
    } else if (event.key === "Enter") {
      event.preventDefault()
      const option = flat[active]
      if (option) choose(option)
    } else if (event.key === "Escape") {
      event.preventDefault()
      event.stopPropagation()
      close()
    } else if (event.key === "Tab") {
      setOpen(false)
    }
  }

  const renderOption = (option: SearchableSelectOption) => {
    const position = positions.get(option.value) ?? -1
    const isSelected = option.value === value
    return (
      <div
        key={option.value}
        id={optionId(position)}
        role="option"
        aria-selected={isSelected}
        onMouseEnter={() => setActive(position)}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => choose(option)}
        className={cn(
          "flex cursor-default items-center justify-between gap-2 rounded-sm px-2 py-1.5 text-sm select-none",
          position === active && "bg-accent text-accent-foreground",
        )}
      >
        <span className="min-w-0">
          <span className="block truncate">{option.label}</span>
          {option.description && (
            <span className="text-muted-foreground block truncate text-xs">
              {option.description}
            </span>
          )}
        </span>
        {isSelected && <IconCheck className="size-4 shrink-0" />}
      </div>
    )
  }

  return (
    <div ref={rootRef} className="min-w-0">
      <button
        ref={triggerRef}
        type="button"
        id={id}
        role="combobox"
        aria-label={label}
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-controls={open ? listId : undefined}
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={(event) => {
          if (!open && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
            event.preventDefault()
            show()
          }
        }}
        className="border-input focus-visible:border-ring focus-visible:ring-ring/50 dark:bg-input/30 flex h-9 w-full min-w-0 items-center justify-between gap-2 rounded-md border bg-transparent px-2.5 text-left text-sm shadow-xs outline-none focus-visible:ring-3 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <span className="min-w-0 truncate">
          {selected ? (
            <>
              {selected.label}
              {selected.description && (
                <span className="text-muted-foreground">
                  {" "}
                  · {selected.description}
                </span>
              )}
            </>
          ) : (
            <span className="text-muted-foreground">{placeholder}</span>
          )}
        </span>
        <IconChevronDown
          className={cn(
            "text-muted-foreground size-4 shrink-0 transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      {open && (
        <div className="bg-popover text-popover-foreground ring-foreground/10 mt-1 flex flex-col overflow-hidden rounded-md shadow-md ring-1">
          <div className="border-border flex items-center gap-2 border-b px-2.5">
            <IconSearch className="text-muted-foreground size-4 shrink-0" />
            <input
              type="search"
              autoFocus
              value={query}
              onChange={(event) => {
                setQuery(event.target.value)
                setActive(0)
              }}
              onKeyDown={onSearchKeyDown}
              placeholder={searchPlaceholder}
              aria-label={searchPlaceholder}
              aria-controls={listId}
              aria-activedescendant={
                flat[active] ? optionId(active) : undefined
              }
              className="placeholder:text-muted-foreground h-9 w-full min-w-0 bg-transparent text-sm outline-none"
            />
          </div>
          <div
            id={listId}
            role="listbox"
            aria-label={label}
            className="max-h-72 overflow-y-auto p-1"
          >
            {leading.map(renderOption)}
            {visibleGroups.map((group) => (
              <div
                key={group.key}
                role="group"
                aria-label={group.label}
                className="pt-1"
              >
                {group.label && (
                  <div
                    aria-hidden="true"
                    className="text-muted-foreground px-2 py-1 text-xs font-semibold"
                  >
                    {group.label}
                  </div>
                )}
                {group.options.map(renderOption)}
              </div>
            ))}
            {visibleGroups.length === 0 && normalized && (
              <p className="text-muted-foreground px-2 py-3 text-center text-sm">
                {emptyText}
              </p>
            )}
            {trailing.length > 0 && (
              <div className="border-border mt-1 border-t pt-1">
                {trailing.map(renderOption)}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
