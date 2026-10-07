package channels

import (
	"net/http"
	"strings"
	"sync"
)

// dynamicServeMux is an http.Handler that supports dynamic registration
// and unregistration of handlers without recreating the server.
type dynamicServeMux struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}

func newDynamicServeMux() *dynamicServeMux {
	return &dynamicServeMux{
		handlers: make(map[string]http.Handler),
	}
}

// Handle registers the handler for the given pattern.
func (dm *dynamicServeMux) Handle(pattern string, handler http.Handler) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.handlers[pattern] = handler
}

// HandleFunc registers the handler function for the given pattern.
func (dm *dynamicServeMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	dm.Handle(pattern, http.HandlerFunc(handler))
}

// Unhandle removes the handler for the given pattern.
func (dm *dynamicServeMux) Unhandle(pattern string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	delete(dm.handlers, pattern)
}

// ServeHTTP dispatches the request to the handler whose pattern best matches
// the request URL path. It supports both exact path matches and subtree
// (trailing-slash) prefix matches, choosing the longest prefix on collision.
func (dm *dynamicServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := dm.handler(r.URL.Path); h != nil {
		h.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// handler returns the handler for path, or nil. The lock covers only the
// lookup: a long request, such as a webhook or a stream, must not stall
// Handle and Unhandle.
func (dm *dynamicServeMux) handler(path string) http.Handler {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// Exact match first.
	if h, ok := dm.handlers[path]; ok {
		return h
	}

	// Longest subtree prefix match (patterns ending with "/").
	var bestLen int
	var bestHandler http.Handler
	for pattern, handler := range dm.handlers {
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(path, pattern) {
			if len(pattern) > bestLen {
				bestLen = len(pattern)
				bestHandler = handler
			}
		}
	}
	return bestHandler
}
