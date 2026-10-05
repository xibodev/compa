import { getDefaultStore } from "jotai"
import { toast } from "sonner"

import { SessionNotFoundError } from "@/api/sessions"
import {
  loadSessionMessages,
  mergeHistoryMessages,
  mergeReconnectedHistory,
} from "@/features/chat/history"
import {
  type WebChatMessage,
  handleWebChatMessage,
} from "@/features/chat/protocol"
import {
  clearStoredSessionId,
  generateSessionId,
  readStoredSessionId,
  writeStoredSessionId,
} from "@/features/chat/state"
import { invalidateSocket, isCurrentSocket } from "@/features/chat/websocket"
import i18n from "@/i18n"
import {
  type ChatAttachment,
  getChatState,
  setChatSelection,
  updateChatStore,
} from "@/store/chat"
import { type GatewayState, gatewayAtom } from "@/store/gateway"

const store = getDefaultStore()

// After sleep or a network change a socket can stay "open" while nothing
// arrives, so the client pings and drops a connection that stays silent.
export const PING_INTERVAL_MS = 25_000
export const PONG_TIMEOUT_MS = 10_000
const HYDRATE_RETRY_MAX_MS = 30_000

let wsRef: WebSocket | null = null
let isConnecting = false
let msgIdCounter = 0
let activeSessionIdRef = getChatState().activeSessionId
let initialized = false
let hydratePromise: Promise<void> | null = null
let hydrateRetryTimer: number | null = null
let hydrateRetryAttempts = 0
let connectionGeneration = 0
let reconnectTimer: number | null = null
let reconnectAttempts = 0
let shouldMaintainConnection = false
let pingTimer: number | null = null
let pongTimer: number | null = null
// The session the last socket opened for. Opening again for the same one is
// a reconnect, and frames sent meanwhile were missed.
let lastOpenedSessionId: string | null = null
// Bumped by every session switch and new chat, so a history load that
// finishes after a later switch is dropped instead of winning.
let sessionChangeToken = 0

function clearReconnectTimer() {
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
}

function stopHeartbeat() {
  if (pingTimer !== null) {
    window.clearInterval(pingTimer)
    pingTimer = null
  }
  if (pongTimer !== null) {
    window.clearTimeout(pongTimer)
    pongTimer = null
  }
}

function clearHydrateRetry() {
  if (hydrateRetryTimer !== null) {
    window.clearTimeout(hydrateRetryTimer)
    hydrateRetryTimer = null
  }
  hydrateRetryAttempts = 0
}

function shouldReconnectFor(generation: number, sessionId: string): boolean {
  return (
    shouldMaintainConnection &&
    generation === connectionGeneration &&
    sessionId === activeSessionIdRef &&
    store.get(gatewayAtom).status === "running"
  )
}

function scheduleReconnect(generation: number, sessionId: string) {
  if (!shouldReconnectFor(generation, sessionId) || reconnectTimer !== null) {
    return
  }

  const delay = Math.min(1000 * 2 ** reconnectAttempts, 5000)
  reconnectAttempts += 1
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = null
    if (!shouldReconnectFor(generation, sessionId)) {
      return
    }
    void connectChat()
  }, delay)
}

/** Closes a connection that stopped answering and schedules a new one. */
function dropConnection(generation: number, sessionId: string) {
  const socket = wsRef
  wsRef = null
  isConnecting = false
  stopHeartbeat()
  invalidateSocket(socket)
  updateChatStore({
    connectionState: "disconnected",
    isTyping: false,
    isTurnActive: false,
  })
  scheduleReconnect(generation, sessionId)
}

function startHeartbeat(
  socket: WebSocket,
  generation: number,
  sessionId: string,
) {
  stopHeartbeat()
  pingTimer = window.setInterval(() => {
    if (
      socket !== wsRef ||
      socket.readyState !== WebSocket.OPEN ||
      pongTimer !== null
    ) {
      return
    }
    try {
      socket.send(JSON.stringify({ type: "ping", id: `ping-${Date.now()}` }))
    } catch {
      // The timeout below drops the connection.
    }
    pongTimer = window.setTimeout(() => {
      pongTimer = null
      if (socket === wsRef) {
        console.warn("Web chat connection stopped answering; reconnecting")
        dropConnection(generation, sessionId)
      }
    }, PONG_TIMEOUT_MS)
  }, PING_INTERVAL_MS)
}

/** Any frame from the server shows the connection is alive. */
function noteServerActivity() {
  if (pongTimer !== null) {
    window.clearTimeout(pongTimer)
    pongTimer = null
  }
}

async function reloadHistoryAfterReconnect(sessionId: string) {
  const token = sessionChangeToken
  try {
    const loaded = await loadSessionMessages(sessionId)
    if (token !== sessionChangeToken || activeSessionIdRef !== sessionId) {
      return
    }
    updateChatStore((prev) => ({
      messages: mergeReconnectedHistory(loaded.messages, prev.messages),
    }))
  } catch (error) {
    // A chat with nothing saved yet has no history to reload; on any other
    // failure the page keeps what it shows.
    if (!(error instanceof SessionNotFoundError)) {
      console.warn("Failed to reload chat history after reconnecting:", error)
    }
  }
}

function needsActiveSessionHydration(): boolean {
  const state = getChatState()
  const storedSessionId = readStoredSessionId()

  return Boolean(
    storedSessionId &&
    storedSessionId === state.activeSessionId &&
    !state.hasHydratedActiveSession,
  )
}

function setActiveSessionId(sessionId: string) {
  activeSessionIdRef = sessionId
  updateChatStore({ activeSessionId: sessionId })
}

function disconnectChatInternal({
  clearDesiredConnection,
}: {
  clearDesiredConnection: boolean
}) {
  connectionGeneration += 1
  clearReconnectTimer()
  stopHeartbeat()

  if (clearDesiredConnection) {
    shouldMaintainConnection = false
  }

  const socket = wsRef
  wsRef = null
  isConnecting = false

  invalidateSocket(socket)

  updateChatStore({
    connectionState: "disconnected",
    isTyping: false,
    isTurnActive: false,
  })
}

export async function connectChat() {
  if (
    store.get(gatewayAtom).status !== "running" ||
    needsActiveSessionHydration()
  ) {
    return
  }

  if (
    isConnecting ||
    (wsRef &&
      (wsRef.readyState === WebSocket.OPEN ||
        wsRef.readyState === WebSocket.CONNECTING))
  ) {
    return
  }

  const generation = connectionGeneration + 1
  connectionGeneration = generation
  isConnecting = true
  clearReconnectTimer()
  updateChatStore({ connectionState: "connecting" })

  try {
    const sessionId = activeSessionIdRef

    if (generation !== connectionGeneration) {
      isConnecting = false
      return
    }

    const wsScheme = window.location.protocol === "https:" ? "wss:" : "ws:"
    const wsUrl = `${wsScheme}//${window.location.host}/web/ws`
    const url = `${wsUrl}?session_id=${encodeURIComponent(sessionId)}`
    const socket = new WebSocket(url)

    if (generation !== connectionGeneration) {
      isConnecting = false
      invalidateSocket(socket)
      return
    }

    socket.onopen = () => {
      if (
        !isCurrentSocket({
          socket,
          currentSocket: wsRef,
          generation,
          currentGeneration: connectionGeneration,
          sessionId,
          currentSessionId: activeSessionIdRef,
        })
      ) {
        return
      }
      updateChatStore({ connectionState: "connected" })
      isConnecting = false
      reconnectAttempts = 0
      startHeartbeat(socket, generation, sessionId)
      if (lastOpenedSessionId === sessionId) {
        void reloadHistoryAfterReconnect(sessionId)
      }
      lastOpenedSessionId = sessionId
    }

    socket.onmessage = (event) => {
      if (
        !isCurrentSocket({
          socket,
          currentSocket: wsRef,
          generation,
          currentGeneration: connectionGeneration,
          sessionId,
          currentSessionId: activeSessionIdRef,
        })
      ) {
        return
      }

      noteServerActivity()
      try {
        const message = JSON.parse(event.data) as WebChatMessage
        handleWebChatMessage(message, sessionId)
      } catch {
        console.warn("Non-JSON message from web chat:", event.data)
      }
    }

    socket.onclose = () => {
      if (
        !isCurrentSocket({
          socket,
          currentSocket: wsRef,
          generation,
          currentGeneration: connectionGeneration,
          sessionId,
          currentSessionId: activeSessionIdRef,
        })
      ) {
        return
      }
      wsRef = null
      isConnecting = false
      stopHeartbeat()
      updateChatStore({
        connectionState: "disconnected",
        isTyping: false,
        isTurnActive: false,
      })
      scheduleReconnect(generation, sessionId)
    }

    socket.onerror = () => {
      if (
        !isCurrentSocket({
          socket,
          currentSocket: wsRef,
          generation,
          currentGeneration: connectionGeneration,
          sessionId,
          currentSessionId: activeSessionIdRef,
        })
      ) {
        return
      }
      isConnecting = false
      updateChatStore({ connectionState: "error" })
      scheduleReconnect(generation, sessionId)
    }

    wsRef = socket
  } catch (error) {
    if (generation !== connectionGeneration) {
      isConnecting = false
      return
    }
    console.error("Failed to connect to web chat:", error)
    updateChatStore({ connectionState: "error" })
    isConnecting = false
    scheduleReconnect(generation, activeSessionIdRef)
  }
}

function scheduleHydrateRetry() {
  // Only the running app retries; a direct call (tests) reports and stops.
  if (!initialized || hydrateRetryTimer !== null) {
    return
  }

  const delay = Math.min(2000 * 2 ** hydrateRetryAttempts, HYDRATE_RETRY_MAX_MS)
  hydrateRetryAttempts += 1
  hydrateRetryTimer = window.setTimeout(() => {
    hydrateRetryTimer = null
    void hydrateActiveSession().then(() => {
      if (shouldMaintainConnection && !needsActiveSessionHydration()) {
        void connectChat()
      }
    })
  }, delay)
}

export async function hydrateActiveSession() {
  if (hydratePromise) {
    return hydratePromise
  }

  const state = getChatState()
  const storedSessionId = readStoredSessionId()

  if (
    !storedSessionId ||
    state.hasHydratedActiveSession ||
    storedSessionId !== state.activeSessionId
  ) {
    if (!state.hasHydratedActiveSession) {
      updateChatStore({ hasHydratedActiveSession: true })
    }
    return
  }

  hydratePromise = loadSessionMessages(storedSessionId)
    .then((loaded) => {
      const currentState = getChatState()
      if (currentState.activeSessionId !== storedSessionId) {
        return
      }
      hydrateRetryAttempts = 0

      if (currentState.messages.length > 0) {
        updateChatStore({
          messages: mergeHistoryMessages(
            loaded.messages,
            currentState.messages,
          ),
          hasHydratedActiveSession: true,
        })
        setChatSelection(storedSessionId, loaded.selection)
        return
      }

      updateChatStore({
        messages: loaded.messages,
        isTyping: false,
        hasHydratedActiveSession: true,
      })
      setChatSelection(storedSessionId, loaded.selection)
    })
    .catch((error) => {
      const missing = error instanceof SessionNotFoundError
      const currentState = getChatState()
      if (currentState.activeSessionId !== storedSessionId) {
        return
      }

      if (!missing) {
        // The conversation exists but could not be shown. It stays the
        // active one but is not connected to, because the next message would
        // continue a conversation the user cannot see; loading is retried.
        console.error("Failed to restore last session history:", error)
        if (hydrateRetryAttempts === 0) {
          toast.error(i18n.t("chat.historyLoadFailed"))
        }
        scheduleHydrateRetry()
        return
      }

      if (currentState.messages.length > 0) {
        updateChatStore({ hasHydratedActiveSession: true })
        return
      }

      // A remembered session that no longer exists, e.g. one that was
      // deleted, is no failure: the chat forgets it and starts fresh.
      setActiveSessionId(generateSessionId())
      // A session is remembered again once it holds a message.
      clearStoredSessionId()
      updateChatStore({
        messages: [],
        isTyping: false,
        hasHydratedActiveSession: true,
      })
    })
    .finally(() => {
      hydratePromise = null
    })

  return hydratePromise
}

interface SendChatMessageInput {
  content: string
  attachments?: ChatAttachment[]
  /**
   * The module the user pointed the agent at for this turn -- the connector
   * gesture. Sent as a `module` field on the payload so the host can scope the
   * turn to that module's capabilities and load its overlay and skills.
   */
  module?: string
  /**
   * Exact instance target or named route for this turn. Empty lets the agent
   * use its configured default model.
   */
  selection?: string
}

export function buildChatMessagePayload({
  content,
  attachments = [],
  module: moduleId,
  selection,
}: SendChatMessageInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    content: content.trim(),
    media: attachments
      .filter((attachment) => attachment.type === "image" && attachment.url)
      .map((attachment) => attachment.url),
  }
  if (moduleId) payload.module = moduleId
  if (selection?.trim()) payload.selection = selection.trim()
  return payload
}

export function sendChatMessage({
  content,
  attachments = [],
  module: moduleId,
  selection,
}: SendChatMessageInput) {
  if (!wsRef || wsRef.readyState !== WebSocket.OPEN) {
    console.warn("WebSocket not connected")
    return false
  }

  const normalizedContent = content.trim()
  const normalizedAttachments = attachments
    .filter((attachment) => attachment.type === "image" && attachment.url)
    .map((attachment) => ({ ...attachment }))

  if (!normalizedContent && normalizedAttachments.length === 0) {
    return false
  }

  const socket = wsRef
  const id = `msg-${++msgIdCounter}-${Date.now()}`
  writeStoredSessionId(activeSessionIdRef)

  updateChatStore((prev) => ({
    messages: [
      ...prev.messages,
      {
        id,
        role: "user",
        content: normalizedContent,
        attachments:
          normalizedAttachments.length > 0 ? normalizedAttachments : undefined,
        timestamp: Date.now(),
      },
    ],
    isTyping: true,
    isTurnActive: true,
  }))

  try {
    const payload = buildChatMessagePayload({
      content: normalizedContent,
      attachments: normalizedAttachments,
      module: moduleId,
      selection,
    })

    socket.send(
      JSON.stringify({
        type: "message.send",
        id,
        payload,
      }),
    )
    return true
  } catch (error) {
    console.error("Failed to send web chat message:", error)
    updateChatStore((prev) => ({
      messages: prev.messages.filter((message) => message.id !== id),
      isTyping: false,
      isTurnActive: false,
    }))
    return false
  }
}

export async function sendChatMessageWhenReady(
  input: SendChatMessageInput,
  timeoutMs = 5000,
) {
  if (sendChatMessage(input)) return true
  await connectChat()
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (getChatState().connectionState === "connected") {
      return sendChatMessage(input)
    }
    await new Promise((resolve) => window.setTimeout(resolve, 100))
  }
  return false
}

export async function switchChatSession(sessionId: string) {
  const token = ++sessionChangeToken
  // Choosing the open chat again cancels a switch still loading, and loads
  // the open chat when its history never arrived.
  if (
    sessionId === activeSessionIdRef &&
    getChatState().hasHydratedActiveSession
  ) {
    return
  }

  try {
    const loaded = await loadSessionMessages(sessionId)
    if (token !== sessionChangeToken) {
      return
    }

    clearHydrateRetry()
    disconnectChatInternal({ clearDesiredConnection: false })
    setActiveSessionId(sessionId)
    updateChatStore({
      messages: loaded.messages,
      isTyping: false,
      hasHydratedActiveSession: true,
      contextUsage: undefined,
    })
    setChatSelection(sessionId, loaded.selection)

    if (store.get(gatewayAtom).status === "running") {
      shouldMaintainConnection = true
      await connectChat()
    }
  } catch (error) {
    if (token !== sessionChangeToken) {
      return
    }
    console.error("Failed to load session history:", error)
    toast.error(i18n.t("chat.historyOpenFailed"))
  }
}

export async function newChatSession() {
  const current = getChatState()
  // An empty chat is already new, unless its history never loaded: then a
  // new chat is the way out of a conversation that cannot be shown.
  if (current.messages.length === 0 && current.hasHydratedActiveSession) {
    return
  }

  sessionChangeToken += 1
  clearHydrateRetry()
  disconnectChatInternal({ clearDesiredConnection: false })
  const sessionId = generateSessionId()
  setActiveSessionId(sessionId)
  setChatSelection(
    sessionId,
    current.selectionBySession[current.activeSessionId] || "",
  )
  clearStoredSessionId()
  updateChatStore({
    messages: [],
    isTyping: false,
    hasHydratedActiveSession: true,
    contextUsage: undefined,
  })

  if (store.get(gatewayAtom).status === "running") {
    shouldMaintainConnection = true
    await connectChat()
  }
}

export function initializeChatStore() {
  if (initialized) {
    return
  }

  initialized = true
  activeSessionIdRef = getChatState().activeSessionId
  let lastGatewayStatus: GatewayState | null = null

  const syncConnectionWithGateway = (force: boolean = false) => {
    const gatewayStatus = store.get(gatewayAtom).status
    if (!force && gatewayStatus === lastGatewayStatus) {
      return
    }
    lastGatewayStatus = gatewayStatus

    if (gatewayStatus === "running") {
      shouldMaintainConnection = true
      if (needsActiveSessionHydration()) {
        return
      }
      void connectChat()
      return
    }

    if (gatewayStatus === "stopped" || gatewayStatus === "error") {
      disconnectChatInternal({ clearDesiredConnection: true })
    }
  }

  store.sub(gatewayAtom, syncConnectionWithGateway)

  if (!readStoredSessionId()) {
    updateChatStore({ hasHydratedActiveSession: true })
    syncConnectionWithGateway(true)
    return
  }

  void hydrateActiveSession().finally(() => {
    syncConnectionWithGateway(true)
  })
}
