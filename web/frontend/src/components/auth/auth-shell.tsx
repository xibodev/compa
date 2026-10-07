import type { ReactNode } from "react"

import { BrandLogo } from "@/components/brand-logo"
import { LanguageMenu, ThemeToggle } from "@/components/header-controls"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { TooltipProvider } from "@/components/ui/tooltip"

interface AuthShellProps {
  title: string
  description: string
  children: ReactNode
}

/** The branded frame of the first-run and sign-in pages. */
export function AuthShell({ title, description, children }: AuthShellProps) {
  return (
    <TooltipProvider>
      <div className="bg-background text-foreground flex min-h-dvh flex-col">
        <header className="border-border/50 flex h-14 shrink-0 items-center justify-end gap-2 border-b px-4">
          <LanguageMenu variant="outline" />
          <ThemeToggle variant="outline" />
        </header>

        <main className="flex flex-1 items-center justify-center p-4">
          <Card className="w-full max-w-md" size="sm">
            <CardHeader className="justify-items-center gap-2 text-center">
              <BrandLogo
                className="mb-1"
                markClassName="size-10"
                nameClassName="text-2xl"
              />
              <CardTitle>
                <h1 className="text-base font-medium">{title}</h1>
              </CardTitle>
              <CardDescription>{description}</CardDescription>
            </CardHeader>
            <CardContent>{children}</CardContent>
          </Card>
        </main>
      </div>
    </TooltipProvider>
  )
}
