package middleware

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultAPIBodyLimit bounds an /api request body unless the route reads a
// larger one itself.
const DefaultAPIBodyLimit = 2 << 20

// APIBodyReadTimeout bounds reading one request body. It is not a server-wide
// ReadTimeout: that one stays armed while the handler runs and cancels long
// requests (module installs, catalog syncs) once it passes.
const APIBodyReadTimeout = 60 * time.Second

// APIBodyLimit caps /api request bodies at limit bytes. Routes in larger
// (exact paths) are capped at their own limit instead: they take uploads
// and check the size themselves. A body declared larger is refused at once.
// Reading a body must finish within APIBodyReadTimeout; the deadline is
// lifted once it is read.
func APIBodyLimit(limit int64, larger map[string]int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && strings.HasPrefix(r.URL.Path, "/api/") {
			n := limit
			if routeLimit, ok := larger[canonicalAuthPath(r.URL.Path)]; ok {
				n = routeLimit
			}
			if r.ContentLength > n {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write([]byte(`{"error":"request body too large"}`))
				return
			}
			r.Body = newDeadlineBody(w, http.MaxBytesReader(w, r.Body, n))
		}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody arms a read deadline on the connection while the body is
// read and lifts it at the body's end, so a slow client cannot hold a
// handler forever and a long handler keeps its connection afterwards.
type deadlineBody struct {
	io.ReadCloser
	controller *http.ResponseController
	once       sync.Once
}

func newDeadlineBody(w http.ResponseWriter, body io.ReadCloser) io.ReadCloser {
	controller := http.NewResponseController(w)
	if controller.SetReadDeadline(time.Now().Add(APIBodyReadTimeout)) != nil {
		// A writer without deadlines (tests, HTTP/2 without support).
		return body
	}
	return &deadlineBody{ReadCloser: body, controller: controller}
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.lift()
	}
	return n, err
}

func (b *deadlineBody) Close() error {
	b.lift()
	return b.ReadCloser.Close()
}

func (b *deadlineBody) lift() {
	b.once.Do(func() { _ = b.controller.SetReadDeadline(time.Time{}) })
}
