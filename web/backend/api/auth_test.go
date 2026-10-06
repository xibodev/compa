package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/web/backend/middleware"
)

type fakePasswordStore struct {
	mu          sync.Mutex
	initialized bool
	password    string
	err         error
}

func (s *fakePasswordStore) IsInitialized(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	return s.initialized, nil
}

func (s *fakePasswordStore) InitializePassword(_ context.Context, plain string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	if s.initialized {
		return false, nil
	}
	s.password = plain
	s.initialized = true
	return true, nil
}

func (s *fakePasswordStore) SetPassword(_ context.Context, plain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.password = plain
	s.initialized = true
	return nil
}

func (s *fakePasswordStore) VerifyPassword(_ context.Context, plain string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	return s.initialized && plain == s.password, nil
}

// testSetupToken is the setup token the auth tests start with.
const testSetupToken = "setup-token-for-tests"

func TestLauncherAuthLoginAndStatus(t *testing.T) {
	const password = "dashboard-test-password"
	const sess = "session-cookie-value"
	store := &fakePasswordStore{initialized: true, password: password}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: sess,
		PasswordStore: store,
	})

	t.Run("status_unauthenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d", rec.Code)
		}
		var body struct {
			Authenticated bool `json:"authenticated"`
			Initialized   bool `json:"initialized"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Authenticated {
			t.Fatalf("unexpected authenticated=true: %+v", body)
		}
	})

	t.Run("login_ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"`+password+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:12345"
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login code = %d body=%s", rec.Code, rec.Body.String())
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != middleware.LauncherDashboardCookieName {
			t.Fatalf("cookies = %#v", cookies)
		}
	})

	t.Run("status_authenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
		req.AddCookie(&http.Cookie{Name: middleware.LauncherDashboardCookieName, Value: sess})
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d", rec.Code)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(`"authenticated":true`)) {
			t.Fatalf("body = %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "token_help") {
			t.Fatalf("authenticated response should omit token_help: %s", rec.Body.String())
		}
	})
}

func TestLauncherAuthUninitializedStoreRequiresSetup(t *testing.T) {
	const sess = "session-cookie-value"
	store := &fakePasswordStore{}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: sess,
		PasswordStore: store,
		SetupToken:    testSetupToken,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Authenticated bool `json:"authenticated"`
		Initialized   bool `json:"initialized"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Initialized {
		t.Fatalf("initialized = true, want false before setup")
	}
	if body.Authenticated {
		t.Fatalf("unexpected authenticated=true: %+v", body)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"not-set-yet"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("login before setup code = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/auth/setup",
		strings.NewReader(`{"password":"12345678","confirm":"12345678","setup_token":"`+testSetupToken+`"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup code = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"12345678"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after setup code = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLauncherAuthSetupRequiresSessionWhenInitialized(t *testing.T) {
	const sess = "session-cookie-value"
	store := &fakePasswordStore{initialized: true, password: "old-password"}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: sess,
		PasswordStore: store,
		SetupToken:    testSetupToken,
	})

	// The setup token does not replace a session once a password exists:
	// the setup is told the password is set.
	body := strings.NewReader(`{"password":"new-password","confirm":"new-password","setup_token":"` + testSetupToken + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || store.password != "old-password" {
		t.Fatalf("setup with the token once set: code = %d body=%s password=%q", rec.Code, rec.Body.String(), store.password)
	}

	body = strings.NewReader(`{"password":"new-password","confirm":"new-password","current_password":"old-password"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/auth/setup", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("change without session code = %d body=%s", rec.Code, rec.Body.String())
	}

	body = strings.NewReader(`{"password":"new-password","confirm":"new-password","current_password":"old-password"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/auth/setup", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: middleware.LauncherDashboardCookieName, Value: sess})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup with session code = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.password != "new-password" {
		t.Fatalf("password = %q, want new-password", store.password)
	}
}

func TestLauncherAuthPasswordChangeNeedsTheCurrentPasswordAndRotatesTheSession(t *testing.T) {
	session, err := middleware.NewLauncherDashboardSession()
	if err != nil {
		t.Fatal(err)
	}
	oldToken := session.Token()
	store := &fakePasswordStore{initialized: true, password: "old-password"}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{Session: session, PasswordStore: store})

	change := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:4000"
		req.AddCookie(&http.Cookie{Name: middleware.LauncherDashboardCookieName, Value: oldToken})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := change(`{"password":"new-password","confirm":"new-password"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("change without the current password = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := change(`{"password":"new-password","confirm":"new-password","current_password":"wrong-password"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("change with a wrong current password = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if store.password != "old-password" {
		t.Fatalf("a refused change saved the password %q", store.password)
	}

	rec := change(`{"password":"new-password","confirm":"new-password","current_password":"old-password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change = %d %s", rec.Code, rec.Body.String())
	}
	if session.Valid(oldToken) {
		t.Fatal("the session was not rotated: other browsers stay signed in")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !session.Valid(cookies[0].Value) {
		t.Fatalf("the browser that changed the password did not get the new session: %#v", cookies)
	}
}

func TestLauncherAuthInitialSetupNeedsTheSetupToken(t *testing.T) {
	store := &fakePasswordStore{}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
		PasswordStore: store,
		SetupToken:    testSetupToken,
	})

	setup := func(token string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/auth/setup",
			strings.NewReader(`{"password":"12345678","confirm":"12345678","setup_token":"`+token+`"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := setup(""); code != http.StatusForbidden {
		t.Fatalf("setup without the token = %d, want 403", code)
	}
	if code := setup("guessed-token"); code != http.StatusForbidden {
		t.Fatalf("setup with a wrong token = %d, want 403", code)
	}
	if store.initialized {
		t.Fatal("a setup without the token stored a password")
	}
	if code := setup(testSetupToken); code != http.StatusOK {
		t.Fatalf("setup with the token = %d, want 200", code)
	}
}

func TestLauncherAuthInitialSetupFailsClosedWithoutAToken(t *testing.T) {
	store := &fakePasswordStore{}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{SessionCookie: "s", PasswordStore: store})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup",
		strings.NewReader(`{"password":"12345678","confirm":"12345678","setup_token":""}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || store.initialized {
		t.Fatalf("setup with no token configured = %d, initialized %t; want 403 and nothing stored", rec.Code, store.initialized)
	}
}

// Two first-run setups racing: exactly one sets the password, the other is
// told it is set already.
func TestLauncherAuthInitialSetupRaceHasOneWinner(t *testing.T) {
	store := &fakePasswordStore{}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
		PasswordStore: store,
		SetupToken:    testSetupToken,
	})

	const racers = 8
	codes := make(chan int, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			password := fmt.Sprintf("racer-password-%d", i)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(
				`{"password":"`+password+`","confirm":"`+password+`","setup_token":"`+testSetupToken+`"}`))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			codes <- rec.Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != racers-1 {
		t.Fatalf("setup answers = %v, want one 200 and %d 409", counts, racers-1)
	}
}

func TestLauncherAuthRejectsPasswordsBcryptCannotHash(t *testing.T) {
	store := &fakePasswordStore{}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{SessionCookie: "s", PasswordStore: store, SetupToken: testSetupToken})

	long := strings.Repeat("é", 37) // 74 bytes, 37 characters
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(
		`{"password":"`+long+`","confirm":"`+long+`","setup_token":"`+testSetupToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("setup with a 74-byte password = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestLauncherAuthLogoutAllSignsEveryBrowserOut(t *testing.T) {
	session, err := middleware.NewLauncherDashboardSession()
	if err != nil {
		t.Fatal(err)
	}
	token := session.Token()
	store := &fakePasswordStore{initialized: true, password: "a-password"}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{Session: session, PasswordStore: store})

	logoutAll := func(cookie string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout-all", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: middleware.LauncherDashboardCookieName, Value: cookie})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := logoutAll("not-the-session"); code != http.StatusUnauthorized {
		t.Fatalf("logout-all without a session = %d, want 401", code)
	}
	if code := logoutAll(token); code != http.StatusOK {
		t.Fatalf("logout-all = %d, want 200", code)
	}
	if session.Valid(token) {
		t.Fatal("logout-all left the old session valid")
	}

	// A browser still holding the old cookie is signed out.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: middleware.LauncherDashboardCookieName, Value: token})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"authenticated":false`) {
		t.Fatalf("status after logout-all = %s", rec.Body.String())
	}
}

func TestLauncherAuthStoreErrorsDoNotRevealPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "s",
		StoreError:    errors.New(`open "C:\Users\someone\.compa\launcher-auth.db": disk I/O error`),
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "launcher-auth.db") || strings.Contains(rec.Body.String(), "someone") {
		t.Fatalf("unauthenticated answer reveals the store path: %s", rec.Body.String())
	}
}

func TestLauncherAuthStoreUnavailableFailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
		StoreError:    errors.New("open auth store"),
	})

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "status", method: http.MethodGet, path: "/api/auth/status"},
		{name: "login", method: http.MethodPost, path: "/api/auth/login", body: `{"password":"password"}`},
		{name: "setup", method: http.MethodPost, path: "/api/auth/setup", body: `{"password":"12345678","confirm":"12345678"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLauncherAuthLogoutRequiresPostAndJSON(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/logout", nil))
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("GET logout: code = %d (expected 404 or 405)", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content-type: code = %d body=%s", rec2.Code, rec2.Body.String())
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/logout", strings.NewReader(`{}`))
	req3.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("POST json logout: code = %d", rec3.Code)
	}
}

func TestLauncherAuthLoginRateLimit(t *testing.T) {
	store := &fakePasswordStore{initialized: true, password: "correct-password"}
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
		PasswordStore: store,
	})

	// 11 failing logins by wrong password; each consumes allow() slot after valid JSON.
	wrongBody := `{"password":"wrong"}`
	for i := 0; i < loginAttemptsPerIP; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(wrongBody))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.168.5.5:9999"
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("iter %d: want 401 got %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(wrongBody))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.5.5:9999"
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt: want 429 got %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginRateLimiterWindow(t *testing.T) {
	l := newLoginRateLimiter()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return t0 }
	for i := 0; i < loginAttemptsPerIP; i++ {
		if !l.allow("ip") {
			t.Fatalf("want allow at %d", i)
		}
	}
	if l.allow("ip") {
		t.Fatal("want deny on 11th")
	}
	l.now = func() time.Time { return t0.Add(loginAttemptWindow + time.Second) }
	if !l.allow("ip") {
		t.Fatal("want allow after window")
	}
}

func TestReferrerPolicyMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := middleware.ReferrerPolicyNoReferrer(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
}

func TestLauncherAuthLogoutEmptyBody(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = http.NoBody
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestLauncherAuthLogoutRejectsTrailingJSON(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{
		SessionCookie: "session-cookie-value",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", strings.NewReader(`{}{}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", rec.Code, rec.Body.String())
	}
}
