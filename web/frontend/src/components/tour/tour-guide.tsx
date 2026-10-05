import { IconChevronLeft, IconChevronRight } from "@tabler/icons-react"
import { useAtom } from "jotai"
import { Dialog as DialogPrimitive } from "radix-ui"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { BrandLogo } from "@/components/brand-logo"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import {
  TOUR_STEPS,
  type TourStep,
  tourAtom,
  tourCurrentStepAtom,
  tourIsActiveAtom,
  useTourActions,
} from "@/store/tour"

interface TourStepConfig {
  title: string
  description: string
  targetSelector?: string
  position: "top" | "bottom" | "left" | "right"
  offsetY?: number
}

export function TourGuide() {
  const { t } = useTranslation()
  const [tourState] = useAtom(tourAtom)
  const [, setCurrentStep] = useAtom(tourCurrentStepAtom)
  const [, setIsActive] = useAtom(tourIsActiveAtom)
  const { goToNextStep, goToPrevStep } = useTourActions()
  // The highlight and the card follow the target when the window resizes.
  const [, setViewportVersion] = useState(0)
  useEffect(() => {
    const onResize = () => setViewportVersion((version) => version + 1)
    window.addEventListener("resize", onResize)
    return () => window.removeEventListener("resize", onResize)
  }, [])

  const steps: Record<TourStep, TourStepConfig> = {
    welcome: {
      title: t("tour.welcome.title"),
      description: t("tour.welcome.description"),
      position: "bottom",
    },
    models: {
      title: t("tour.models.title"),
      description: t("tour.models.description"),
      targetSelector: "[data-tour='models-nav']",
      position: "right",
    },
    completed: {
      title: "",
      description: "",
      position: "bottom",
    },
  }

  // A stored step this version does not have ends the tour.
  const currentConfig: TourStepConfig | undefined = steps[tourState.currentStep]
  if (
    !tourState.isActive ||
    tourState.currentStep === "completed" ||
    !currentConfig
  ) {
    return null
  }

  const currentStepIndex = TOUR_STEPS.indexOf(tourState.currentStep)
  const totalSteps = TOUR_STEPS.length - 1

  const handleNext = () => {
    const nextStep = goToNextStep(tourState.currentStep)
    setCurrentStep(nextStep)
    if (nextStep === "completed") {
      setIsActive(false)
    }
  }

  const handlePrev = () => {
    const prevStep = goToPrevStep(tourState.currentStep)
    setCurrentStep(prevStep)
  }

  const handleSkip = () => {
    setCurrentStep("completed")
    setIsActive(false)
  }

  const getTargetElement = () => {
    if (!currentConfig.targetSelector) return null
    return document.querySelector(currentConfig.targetSelector)
  }

  const targetElement = getTargetElement()

  const getPopoverPosition = () => {
    if (!targetElement) {
      return {
        top: "50%",
        left: "50%",
        transform: "translate(-50%, -50%)",
      }
    }

    const rect = targetElement.getBoundingClientRect()
    const offset = 12
    const offsetY = currentConfig.offsetY ?? 0

    switch (currentConfig.position) {
      case "top":
        return {
          top: rect.top - offset,
          left: rect.left + rect.width / 2,
          transform: "translate(-50%, -100%)",
        }
      case "bottom":
        return {
          top: rect.bottom + offset,
          left: rect.left + rect.width / 2,
          transform: "translateX(-50%)",
        }
      case "left":
        return {
          top: rect.top + rect.height / 2 + offsetY,
          left: rect.left - offset,
          transform: "translate(-100%, -50%)",
        }
      case "right":
        return {
          top: rect.top + rect.height / 2 + offsetY,
          left: rect.right + offset,
          transform: "translateY(-50%)",
        }
      default:
        return {
          top: rect.bottom + offset,
          left: rect.left + rect.width / 2,
          transform: "translateX(-50%)",
        }
    }
  }

  const position = getPopoverPosition()
  const isCentered = !targetElement

  return (
    <>
      {targetElement ? (
        <div
          className="pointer-events-none fixed z-[100] transition-all duration-300"
          style={{
            top: targetElement.getBoundingClientRect().top - 8,
            left: targetElement.getBoundingClientRect().left - 8,
            width: targetElement.getBoundingClientRect().width + 16,
            height: targetElement.getBoundingClientRect().height + 16,
            boxShadow:
              "0 0 0 9999px rgba(0, 0, 0, 0.2), 0 0 2px 9999px rgba(0, 0, 0, 0.1)",
            borderRadius: "12px",
          }}
        />
      ) : (
        <div className="fixed inset-0 z-[100] bg-black/20 backdrop-blur-[2px]" />
      )}

      {targetElement && (
        <div
          className="ring-primary ring-offset-background pointer-events-none fixed z-[101] rounded-lg ring-2 ring-offset-2 transition-all duration-300"
          style={{
            top: targetElement.getBoundingClientRect().top - 4,
            left: targetElement.getBoundingClientRect().left - 4,
            width: targetElement.getBoundingClientRect().width + 8,
            height: targetElement.getBoundingClientRect().height + 8,
          }}
        />
      )}

      {/* A dialog primitive gives the card focus on open, Escape to skip, and
          a focus trap while it covers the page. A step that points at the
          sidebar leaves the page usable, so the user can follow it. */}
      <DialogPrimitive.Root
        open
        modal={isCentered}
        onOpenChange={(open) => {
          if (!open) handleSkip()
        }}
      >
        <DialogPrimitive.Content
          aria-modal={isCentered || undefined}
          onPointerDownOutside={(event) => event.preventDefault()}
          onInteractOutside={(event) => event.preventDefault()}
          className={cn(
            "bg-background fixed z-[102] w-80 max-w-[calc(100vw-2rem)] rounded-xl border p-4 shadow-2xl outline-none",
            isCentered && "max-w-md",
          )}
          style={position}
        >
          <div className="mb-3 flex items-center gap-2">
            {tourState.currentStep === "welcome" && (
              <BrandLogo withName={false} decorative markClassName="size-6" />
            )}
            <DialogPrimitive.Title asChild>
              <h3 className="font-semibold">{currentConfig.title}</h3>
            </DialogPrimitive.Title>
          </div>

          <DialogPrimitive.Description asChild>
            <p className="text-muted-foreground mb-4 text-sm leading-relaxed">
              {currentConfig.description}
            </p>
          </DialogPrimitive.Description>

          <div className="flex items-center justify-between">
            <div className="text-muted-foreground text-xs">
              {currentStepIndex + 1} / {totalSteps}
            </div>

            <div className="flex items-center gap-2">
              {currentStepIndex > 0 && (
                <Button variant="outline" size="sm" onClick={handlePrev}>
                  <IconChevronLeft className="size-4" />
                  {t("tour.prev")}
                </Button>
              )}
              <Button size="sm" onClick={handleNext}>
                {currentStepIndex === totalSteps - 1
                  ? t("tour.finish")
                  : t("tour.next")}
                {currentStepIndex < totalSteps - 1 && (
                  <IconChevronRight className="size-4" />
                )}
              </Button>
            </div>
          </div>

          {currentStepIndex < totalSteps - 1 && (
            <Button
              variant="link"
              size="sm"
              className="mt-2 h-auto p-0 text-xs"
              onClick={handleSkip}
            >
              {t("tour.skip")}
            </Button>
          )}
        </DialogPrimitive.Content>
      </DialogPrimitive.Root>
    </>
  )
}
