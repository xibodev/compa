import { createContext } from "react"

/**
 * The remote images a person chose to load in one message, so that they stay
 * loaded while the message re-renders as it streams. Each message has its
 * own: a click loads an image in that message only. Without one, it loads
 * where it was clicked only.
 */
export const RevealedImagesContext = createContext<Set<string> | null>(null)
