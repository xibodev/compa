package launcherconfig

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPasswordStoreInitializeIsInsertOnly(t *testing.T) {
	store := NewPasswordStore(filepath.Join(t.TempDir(), FileName), Default())
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
	if ok, _ := store.VerifyPassword(ctx, "later-password"); ok {
		t.Fatal("a later setup replaced the first password")
	}
}

func TestUpdateKeepsConcurrentChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	store := NewPasswordStore(path, Default())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := store.SetPassword(context.Background(), "a-password"); err != nil {
			t.Errorf("SetPassword() error = %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := Update(path, Default(), func(cfg *Config) error {
			cfg.AllowedHosts = []string{"compa.example.com"}
			return nil
		}); err != nil {
			t.Errorf("Update() error = %v", err)
		}
	}()
	wg.Wait()

	cfg, err := Read(path, Default())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DashboardPasswordHash == "" || len(cfg.AllowedHosts) != 1 {
		t.Fatalf("a concurrent writer dropped a change: hash set %t, allowed_hosts %v",
			cfg.DashboardPasswordHash != "", cfg.AllowedHosts)
	}
}
