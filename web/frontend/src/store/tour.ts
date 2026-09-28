import { atom } from "jotai"
import { atomWithStorage } from "jotai/utils"

export type TourStep = "welcome" | "models" | "completed"

/** The tour's steps in order; "completed" ends it. */
export const TOUR_STEPS: readonly TourStep[] = [
  "welcome",
  "models",
  "completed",
]

export interface TourState {
  currentStep: TourStep
  isActive: boolean
}

const STORAGE_KEY = "compa-tour-state"

const DEFAULT_TOUR_STATE: TourState = {
  currentStep: "welcome",
  isActive: true,
}

export const tourAtom = atomWithStorage<TourState>(
  STORAGE_KEY,
  DEFAULT_TOUR_STATE,
)

export const tourIsActiveAtom = atom(
  (get) => get(tourAtom).isActive,
  (get, set, isActive: boolean) => {
    set(tourAtom, { ...get(tourAtom), isActive })
  },
)

export const tourCurrentStepAtom = atom(
  (get) => get(tourAtom).currentStep,
  (get, set, step: TourStep) => {
    set(tourAtom, { ...get(tourAtom), currentStep: step })
  },
)

export function useTourActions() {
  const goToNextStep = (currentStep: TourStep): TourStep => {
    const currentIndex = TOUR_STEPS.indexOf(currentStep)
    if (currentIndex >= 0 && currentIndex < TOUR_STEPS.length - 1) {
      return TOUR_STEPS[currentIndex + 1]
    }
    return "completed"
  }

  const goToPrevStep = (currentStep: TourStep): TourStep => {
    const currentIndex = TOUR_STEPS.indexOf(currentStep)
    if (currentIndex > 0) {
      return TOUR_STEPS[currentIndex - 1]
    }
    return currentStep
  }

  return { goToNextStep, goToPrevStep }
}
