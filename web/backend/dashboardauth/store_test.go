//go:build !mipsle && !netbsd && !(freebsd && arm)

package dashboardauth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInitializePasswordIsInsertOnly(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := store.InitializePassword(ctx, "first-password-"+string(rune('a'+i)))
			if err != nil {
				t.Errorf("InitializePassword() error = %v", err)
			}
			if created {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d setups succeeded, want exactly 1", wins.Load())
	}

	created, err := store.InitializePassword(ctx, "later-password")
	if err != nil || created {
		t.Fatalf("InitializePassword() after setup = %t, %v; want false, nil", created, err)
	}
	if ok, err := store.VerifyPassword(ctx, "later-password"); err != nil || ok {
		t.Fatalf("VerifyPassword(later) = %t, %v; the first password must stay", ok, err)
	}

	// Changing the password is still an update.
	if err := store.SetPassword(ctx, "changed-password"); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	if ok, err := store.VerifyPassword(ctx, "changed-password"); err != nil || !ok {
		t.Fatalf("VerifyPassword(changed) = %t, %v", ok, err)
	}
}
