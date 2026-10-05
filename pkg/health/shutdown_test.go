package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestShutdownHandler_RequiresTheToken(t *testing.T) {
	s := newTestServer()
	called := make(chan struct{}, 1)
	s.SetShutdownFunc(func() { called <- struct{}{} })

	for _, header := range []string{"", "Bearer wrong", "test"} {
		req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		s.shutdownHandler(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("shutdown with Authorization %q = %d, want %d", header, w.Code, http.StatusUnauthorized)
		}
	}
	select {
	case <-called:
		t.Fatal("shutdown ran without the token")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestShutdownHandler_RefusesWithoutAConfiguredToken(t *testing.T) {
	s := newTestServer()
	s.authToken = ""
	s.SetShutdownFunc(func() { t.Error("shutdown ran on a server without a token") })

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	s.shutdownHandler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("shutdown without a configured token = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestShutdownHandler_RunsTheShutdownAfterAnswering(t *testing.T) {
	s := newTestServer()
	called := make(chan struct{}, 1)
	s.SetShutdownFunc(func() { called <- struct{}{} })

	mux := http.NewServeMux()
	s.RegisterOnMux(mux)

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/shutdown", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /shutdown = %d, want %d", get.Code, http.StatusMethodNotAllowed)
	}

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /shutdown = %d, want %d", w.Code, http.StatusAccepted)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown function was not called")
	}
}

func TestShutdownHandler_NotConfigured(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	s.shutdownHandler(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown without a func = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
