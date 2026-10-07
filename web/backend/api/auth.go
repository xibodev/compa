package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/web/backend/middleware"
)

const (
	// minDashboardPasswordRunes is the shortest dashboard password.
	minDashboardPasswordRunes = 8
	// maxDashboardPasswordBytes is bcrypt's limit: it ignores, or refuses,
	// anything longer.
	maxDashboardPasswordBytes = 72
	// authBodyMaxBytes bounds an auth request body.
	authBodyMaxBytes = 64 << 10
)

// PasswordStore is the interface for dashboard password persistence.
// Implemented by dashboardauth.Store and launcherconfig.PasswordStore.
type PasswordStore interface {
	IsInitialized(ctx context.Context) (bool, error)
	// InitializePassword stores the first password. It reports false and
	// changes nothing when a password is stored already.
	InitializePassword(ctx context.Context, plain string) (bool, error)
	SetPassword(ctx context.Context, plain string) error
	VerifyPassword(ctx context.Context, plain string) (bool, error)
}

// LauncherAuthRouteOpts configures dashboard auth handlers.
type LauncherAuthRouteOpts struct {
	// Session is the process session every sign-in shares; rotating it signs
	// every browser out. When nil, SessionCookie is a fixed session token.
	Session       *middleware.LauncherDashboardSession
	SessionCookie string
	SecureCookie  func(*http.Request) bool
	// PasswordStore enables password login. It must be non-nil for auth to work.
	PasswordStore PasswordStore
	// StoreError holds the error returned when opening the password store. When
	// non-nil and PasswordStore is nil, auth endpoints fail closed with a
	// recovery message.
	StoreError error
	// SetupToken must accompany the first password. Only the launcher's own
	// browser launch and console show it; without one, first-run setup is
	// refused.
	SetupToken string
	// TrustedProxyCIDRs are the proxies whose X-Forwarded-For names the
	// client that login attempts are limited by. A list that does not parse
	// counts by peer address; the network policy refuses to start with one.
	TrustedProxyCIDRs []string
}

type launcherAuthLoginBody struct {
	Password string `json:"password"`
}

type launcherAuthSetupBody struct {
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
	// CurrentPassword is required to change a password that is set.
	CurrentPassword string `json:"current_password"`
	// SetupToken is required to set the first password.
	SetupToken string `json:"setup_token"`
}

type launcherAuthStatusResponse struct {
	Authenticated bool `json:"authenticated"`
	Initialized   bool `json:"initialized"`
}

// RegisterLauncherAuthRoutes registers /api/auth/login|logout|logout-all|status|setup.
func RegisterLauncherAuthRoutes(mux *http.ServeMux, opts LauncherAuthRouteOpts) {
	secure := opts.SecureCookie
	if secure == nil {
		secure = middleware.DefaultLauncherDashboardSecureCookie
	}
	session := opts.Session
	if session == nil {
		session = middleware.FixedLauncherDashboardSession(opts.SessionCookie)
	}
	clientIPs, _ := middleware.NewClientIPResolver(opts.TrustedProxyCIDRs)
	h := &launcherAuthHandlers{
		session:      session,
		secureCookie: secure,
		store:        opts.PasswordStore,
		storeErr:     opts.StoreError,
		setupToken:   opts.SetupToken,
		loginLimit:   newLoginRateLimiter(),
		clientIPs:    clientIPs,
	}
	mux.HandleFunc("POST /api/auth/login", h.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)
	mux.HandleFunc("POST /api/auth/logout-all", h.handleLogoutAll)
	mux.HandleFunc("GET /api/auth/status", h.handleStatus)
	mux.HandleFunc("POST /api/auth/setup", h.handleSetup)
}

type launcherAuthHandlers struct {
	session      *middleware.LauncherDashboardSession
	secureCookie func(*http.Request) bool
	store        PasswordStore
	storeErr     error // set when the store failed to open; drives recovery messages
	setupToken   string
	loginLimit   *loginRateLimiter
	// clientIPs names the client login attempts are counted against; nil
	// counts by peer address.
	clientIPs *middleware.ClientIPResolver
}

// errPasswordStoreUnavailable is what unauthenticated callers learn of a
// store failure; the launcher log has the details, such as the file path.
const errPasswordStoreUnavailable = "password store unavailable; to recover, stop Compa, reset the dashboard password storage and start it again (see the launcher log)"

// isStoreInitialized safely queries the store.
// Returns (false, err) on store errors — callers must treat this as a 5xx, not as
// "uninitialized", to keep auth fail-closed.
func (h *launcherAuthHandlers) isStoreInitialized(ctx context.Context) (bool, error) {
	if h.store == nil {
		if h.storeErr != nil {
			return false, fmt.Errorf("password store unavailable: %w", h.storeErr)
		}
		return false, errors.New("password store not configured")
	}
	return h.store.IsInitialized(ctx)
}

// storeUnavailable logs err and answers 503 without its details.
func storeUnavailable(w http.ResponseWriter, err error) {
	logger.ErrorC("web", fmt.Sprintf("Dashboard password store: %v", err))
	writeJSONError(w, http.StatusServiceUnavailable, errPasswordStoreUnavailable)
}

func (h *launcherAuthHandlers) authenticated(r *http.Request) bool {
	c, err := r.Cookie(middleware.LauncherDashboardCookieName)
	return err == nil && h.session.Valid(c.Value)
}

func (h *launcherAuthHandlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body launcherAuthLoginBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, authBodyMaxBytes)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !h.loginLimit.allow(clientIPForLimiter(r, h.clientIPs)) {
		writeJSONError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	in := strings.TrimSpace(body.Password)

	initialized, initErr := h.isStoreInitialized(r.Context())
	if initErr != nil {
		storeUnavailable(w, initErr)
		return
	}
	if !initialized {
		writeJSONError(w, http.StatusConflict, "password has not been set")
		return
	}
	// No stored password is longer, and bcrypt refuses to compare one.
	if len(in) > maxDashboardPasswordBytes {
		writeJSONError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	ok, err := h.store.VerifyPassword(r.Context(), in)
	if err != nil {
		storeUnavailable(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	middleware.SetLauncherDashboardSessionCookie(w, r, h.session.Token(), h.secureCookie)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *launcherAuthHandlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !h.readEmptyJSONBody(w, r) {
		return
	}
	middleware.ClearLauncherDashboardSessionCookie(w, r, h.secureCookie)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleLogoutAll signs every browser out: it rotates the session token all
// sign-ins share, then clears this browser's cookie.
//
//	POST /api/auth/logout-all
func (h *launcherAuthHandlers) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	if !h.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.readEmptyJSONBody(w, r) {
		return
	}
	if _, err := h.session.Rotate(); err != nil {
		logger.ErrorC("web", fmt.Sprintf("Rotating the dashboard session failed: %v", err))
		writeJSONError(w, http.StatusInternalServerError, "could not sign out other browsers")
		return
	}
	middleware.ClearLauncherDashboardSessionCookie(w, r, h.secureCookie)
	logger.InfoC("web", "Signed every browser out of the dashboard")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readEmptyJSONBody accepts an empty body or one JSON object. A JSON content
// type keeps plain HTML forms on other sites from posting here.
func (h *launcherAuthHandlers) readEmptyJSONBody(w http.ResponseWriter, r *http.Request) bool {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if !strings.HasPrefix(ct, "application/json") {
		writeJSONError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, logoutBodyMaxBytes))
	if err := dec.Decode(&struct{}{}); err != nil && err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (h *launcherAuthHandlers) handleStatus(w http.ResponseWriter, r *http.Request) {
	initialized, initErr := h.isStoreInitialized(r.Context())
	if initErr != nil {
		storeUnavailable(w, initErr)
		return
	}
	writeJSON(w, http.StatusOK, launcherAuthStatusResponse{
		Authenticated: h.authenticated(r),
		Initialized:   initialized,
	})
}

// handleSetup sets or changes the dashboard password.
//
// Rules:
//   - The first password needs the setup token the launcher opened or printed,
//     so only the person who started Compa can set it. It is stored only when
//     none exists: of two setups racing, one wins and the other gets 409.
//   - Changing a password needs a session and the current password. The
//     change signs every other browser out; this one gets the new session.
func (h *launcherAuthHandlers) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !middleware.SameOriginRequest(r) {
		writeJSONError(w, http.StatusForbidden, "cross-site setup request rejected")
		return
	}
	if h.store == nil {
		_, err := h.isStoreInitialized(r.Context())
		storeUnavailable(w, err)
		return
	}

	var body launcherAuthSetupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, authBodyMaxBytes)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	initialized, initErr := h.isStoreInitialized(r.Context())
	if initErr != nil {
		storeUnavailable(w, initErr)
		return
	}
	if !initialized {
		h.setFirstPassword(w, r, body)
		return
	}
	// A first-run setup that lost the race, or an old setup link: the
	// password exists, and the setup token never replaces a session.
	if strings.TrimSpace(body.SetupToken) != "" && strings.TrimSpace(body.CurrentPassword) == "" {
		writeJSONError(w, http.StatusConflict, "the dashboard password has already been set")
		return
	}
	h.changePassword(w, r, body)
}

func (h *launcherAuthHandlers) setFirstPassword(w http.ResponseWriter, r *http.Request, body launcherAuthSetupBody) {
	if !h.validSetupToken(body.SetupToken) {
		writeJSONError(w, http.StatusForbidden, "setup token missing or wrong; open the setup link Compa opened or printed when it started")
		return
	}
	pw, ok := validNewPassword(w, body)
	if !ok {
		return
	}
	created, err := h.store.InitializePassword(r.Context(), pw)
	if err != nil {
		logger.ErrorC("web", fmt.Sprintf("Saving the first dashboard password failed: %v", err))
		writeJSONError(w, http.StatusInternalServerError, "failed to save password")
		return
	}
	if !created {
		writeJSONError(w, http.StatusConflict, "the dashboard password has already been set")
		return
	}
	logger.InfoC("web", "Dashboard password set")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *launcherAuthHandlers) changePassword(w http.ResponseWriter, r *http.Request, body launcherAuthSetupBody) {
	if !h.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "must be authenticated to change password")
		return
	}
	pw, ok := validNewPassword(w, body)
	if !ok {
		return
	}
	if !h.loginLimit.allow(clientIPForLimiter(r, h.clientIPs)) {
		writeJSONError(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	current := strings.TrimSpace(body.CurrentPassword)
	if current == "" {
		writeJSONError(w, http.StatusBadRequest, "current password is required")
		return
	}
	matches := false
	if len(current) <= maxDashboardPasswordBytes {
		var err error
		if matches, err = h.store.VerifyPassword(r.Context(), current); err != nil {
			storeUnavailable(w, err)
			return
		}
	}
	if !matches {
		writeJSONError(w, http.StatusForbidden, "current password is incorrect")
		return
	}

	if err := h.store.SetPassword(r.Context(), pw); err != nil {
		logger.ErrorC("web", fmt.Sprintf("Changing the dashboard password failed: %v", err))
		writeJSONError(w, http.StatusInternalServerError, "failed to save password")
		return
	}
	// A changed password ends every other sign-in; this browser keeps going
	// with the new session.
	token, err := h.session.Rotate()
	if err != nil {
		logger.ErrorC("web", fmt.Sprintf("Rotating the dashboard session failed: %v", err))
		writeJSONError(w, http.StatusInternalServerError, "password changed, but other browsers stay signed in")
		return
	}
	middleware.SetLauncherDashboardSessionCookie(w, r, token, h.secureCookie)
	logger.InfoC("web", "Dashboard password changed; other browsers were signed out")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *launcherAuthHandlers) validSetupToken(given string) bool {
	given = strings.TrimSpace(given)
	return h.setupToken != "" && given != "" &&
		subtle.ConstantTimeCompare([]byte(given), []byte(h.setupToken)) == 1
}

// validNewPassword returns the trimmed new password, or answers 400 and
// false when it is empty, unconfirmed, too short, or too long for bcrypt.
func validNewPassword(w http.ResponseWriter, body launcherAuthSetupBody) (string, bool) {
	pw := strings.TrimSpace(body.Password)
	if pw != "" && pw != strings.TrimSpace(body.Confirm) {
		writeJSONError(w, http.StatusBadRequest, "passwords do not match")
		return "", false
	}
	if err := ValidateDashboardPassword(pw); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return pw, true
}

// ValidateDashboardPassword checks a trimmed dashboard password: at least 8
// characters and at most 72 bytes, the longest bcrypt hashes. The web setup
// and the -password command apply the same rule.
func ValidateDashboardPassword(pw string) error {
	switch {
	case pw == "":
		return errors.New("password must not be empty")
	case len([]rune(pw)) < minDashboardPasswordRunes:
		return fmt.Errorf("password must be at least %d characters", minDashboardPasswordRunes)
	case len(pw) > maxDashboardPasswordBytes:
		return fmt.Errorf("password must be at most %d bytes", maxDashboardPasswordBytes)
	}
	return nil
}
