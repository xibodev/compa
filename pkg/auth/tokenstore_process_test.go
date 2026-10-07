package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/llm-provider-auth/tokenstore"
)

const (
	refreshHelperEnv      = "COMPA_REFRESH_HELPER"
	refreshHelperURLEnv   = "COMPA_REFRESH_HELPER_URL"
	refreshHelperStoreEnv = "COMPA_REFRESH_HELPER_STORE"
	refreshProcesses      = 4
	refreshKey            = "signed-in"
)

// rotatingTokenEndpoint issues a new refresh token on every refresh and
// rejects a spent one, as providers that detect reuse do.
type rotatingTokenEndpoint struct {
	mu       sync.Mutex
	current  string
	requests int
	reused   int
}

func (e *rotatingTokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests++
	if string(body) != e.current {
		e.reused++
		http.Error(w, "refresh token reused", http.StatusBadRequest)
		return
	}
	e.current = fmt.Sprintf("refresh-%d", e.requests+1)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"access_token":  fmt.Sprintf("access-%d", e.requests+1),
		"refresh_token": e.current,
	})
}

func httpRefresh(endpoint string) tokenstore.RefreshFunc {
	return func(ctx context.Context, current tokenstore.Record) (tokenstore.Record, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(current.RefreshToken))
		if err != nil {
			return tokenstore.Record{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return tokenstore.Record{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return tokenstore.Record{}, fmt.Errorf("refresh status %d", resp.StatusCode)
		}
		var tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
			return tokenstore.Record{}, err
		}
		next := current.Clone()
		next.AccessToken = tokens.AccessToken
		next.RefreshToken = tokens.RefreshToken
		next.Expiry = time.Now().Add(time.Hour)
		return next, nil
	}
}

// TestRefreshHelperProcess is the body of each helper process started by
// TestCoordinatorRefreshesOnceAcrossProcesses.
func TestRefreshHelperProcess(t *testing.T) {
	if os.Getenv(refreshHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	store := OpenTokenStore(os.Getenv(refreshHelperStoreEnv))
	coordinator, err := tokenstore.NewCoordinator(store, httpRefresh(os.Getenv(refreshHelperURLEnv)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	record, err := coordinator.Token(context.Background(), refreshKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(record.AccessToken)
}

func TestCoordinatorRefreshesOnceAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts helper processes")
	}
	endpoint := &rotatingTokenEndpoint{current: "refresh-1"}
	server := httptest.NewServer(endpoint)
	defer server.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	store := OpenTokenStore(path)
	if _, err := store.Save(context.Background(), refreshKey, tokenstore.Record{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(-time.Minute),
		AccountID:    "account",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	type result struct {
		out string
		err error
	}
	results := make(chan result, refreshProcesses)
	for range refreshProcesses {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRefreshHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			refreshHelperEnv+"=1",
			refreshHelperURLEnv+"="+server.URL,
			refreshHelperStoreEnv+"="+path,
		)
		cmd.Stderr = os.Stderr
		go func() {
			out, err := cmd.Output()
			results <- result{out: string(out), err: err}
		}()
	}

	var tokens []string
	for range refreshProcesses {
		got := <-results
		if got.err != nil {
			t.Fatalf("helper failed: %v", got.err)
		}
		// The test binary prints PASS after the helper's own output.
		tokens = append(tokens, strings.SplitN(got.out, "\n", 2)[0])
	}

	endpoint.mu.Lock()
	requests, reused := endpoint.requests, endpoint.reused
	endpoint.mu.Unlock()
	if requests != 1 || reused != 0 {
		t.Fatalf("token endpoint saw %d refreshes (%d reusing a spent token), want exactly 1", requests, reused)
	}
	for _, token := range tokens {
		if token != "access-2" {
			t.Fatalf("processes ended with tokens %q, want all access-2", tokens)
		}
	}
	stored, err := store.Load(context.Background(), refreshKey)
	if err != nil || stored.RefreshToken != "refresh-2" {
		t.Fatalf("stored = %v, %v", stored, err)
	}
}
