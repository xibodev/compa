package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/xibodev/compa/v2/pkg/logger"
)

// JSONContentType sets the Content-Type header to application/json for
// API requests handled by the wrapped handler.
func JSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}

// responseRecorder wraps http.ResponseWriter to capture the status code.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying ResponseWriter if it implements http.Flusher.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter so that http.ResponseController
// and interface checks (like http.Flusher) can see through the wrapper.
func (rr *responseRecorder) Unwrap() http.ResponseWriter {
	return rr.ResponseWriter
}

// Hijack implements http.Hijacker so that WebSocket upgrades work through
// the middleware layer.
func (rr *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rr.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Logger logs each HTTP request with method, path, status code, and duration.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.DebugC("http", fmt.Sprintf("%s %s %d %s", r.Method, r.URL.Path, rec.statusCode, time.Since(start)))
	})
}

// Recoverer recovers from panics in downstream handlers and returns a 500
// Internal Server Error response when nothing was sent yet.
//
// http.ErrAbortHandler is not a crash: a reverse proxy raises it when the
// client goes away mid-copy, so it is re-panicked for net/http to handle
// quietly. A response whose headers already went out is left as it is;
// writing an error into it would only corrupt it.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &headerTrackingWriter{ResponseWriter: w}
		defer func() {
			if err := recover(); err != nil {
				if err == http.ErrAbortHandler {
					panic(err)
				}
				logger.RecoverPanicNoExit(err)
				logger.ErrorC("http", fmt.Sprintf("panic recovered: %v\n%s", err, debug.Stack()))
				if !tracked.wroteHeader {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"internal server error"}`))
				}
			}
		}()
		next.ServeHTTP(tracked, r)
	})
}

// headerTrackingWriter records whether the response headers were sent.
type headerTrackingWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (tw *headerTrackingWriter) WriteHeader(code int) {
	// 1xx informational answers precede the real one.
	if code >= 200 {
		tw.wroteHeader = true
	}
	tw.ResponseWriter.WriteHeader(code)
}

func (tw *headerTrackingWriter) Write(b []byte) (int, error) {
	tw.wroteHeader = true
	return tw.ResponseWriter.Write(b)
}

// Flush sends the headers, then delegates.
func (tw *headerTrackingWriter) Flush() {
	tw.wroteHeader = true
	if f, ok := tw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack hands the connection over; nothing may be written after it.
func (tw *headerTrackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := tw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	tw.wroteHeader = true
	return hj.Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (tw *headerTrackingWriter) Unwrap() http.ResponseWriter {
	return tw.ResponseWriter
}
