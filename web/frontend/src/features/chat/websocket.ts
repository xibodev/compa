export function invalidateSocket(socket: WebSocket | null) {
  if (!socket) {
    return
  }

  socket.onopen = null
  socket.onmessage = null
  socket.onclose = null
  socket.onerror = null
  socket.close()
}

export function isCurrentSocket({
  socket,
  currentSocket,
  generation,
  currentGeneration,
  sessionId,
  currentSessionId,
}: {
  socket: WebSocket
  currentSocket: WebSocket | null
  generation: number
  currentGeneration: number
  sessionId: string
  currentSessionId: string
}): boolean {
  return (
    currentSocket === socket &&
    generation === currentGeneration &&
    sessionId === currentSessionId
  )
}
