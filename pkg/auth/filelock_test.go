package auth

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/xibodev/compa/v2/pkg/config"
)

const (
	lockHelperEnv        = "COMPA_AUTH_LOCK_HELPER"
	lockHelperIterations = 25
	lockHelperProcesses  = 4
	lockCounterKey       = "lock-counter"
)

// incrementCounter performs one read-modify-write of the counter credential
// under the auth store locks, as a login or logout does.
func incrementCounter() error {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return withAuthFileLock(func() error {
		current, err := getCredentialUnlocked(lockCounterKey)
		if err != nil {
			return err
		}
		count := 0
		if current != nil {
			if count, err = strconv.Atoi(current.AccessToken); err != nil {
				return err
			}
		}
		return setCredentialUnlocked(lockCounterKey, &AuthCredential{
			AccessToken: strconv.Itoa(count + 1),
			Provider:    lockCounterKey,
			AuthMethod:  "token",
		})
	})
}

// TestAuthFileLockHelperProcess is the body of each helper process started by
// TestAuthFileLockSerializesProcesses; it does nothing in a normal run.
func TestAuthFileLockHelperProcess(t *testing.T) {
	if os.Getenv(lockHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	for range lockHelperIterations {
		if err := incrementCounter(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func TestAuthFileLockSerializesProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts helper processes")
	}
	home := filepath.Join(t.TempDir(), ".compa")
	t.Setenv(config.EnvHome, home)

	cmds := make([]*exec.Cmd, 0, lockHelperProcesses)
	for range lockHelperProcesses {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAuthFileLockHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), lockHelperEnv+"=1", config.EnvHome+"="+home)
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}

	cred, err := GetCredential(lockCounterKey)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	want := strconv.Itoa(lockHelperProcesses * lockHelperIterations)
	if cred == nil || cred.AccessToken != want {
		t.Fatalf("counter = %v, want %s (lost updates across processes)", cred, want)
	}
}

func TestAuthFileLockReleasedWhenHolderExits(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a helper process")
	}
	home := filepath.Join(t.TempDir(), ".compa")
	t.Setenv(config.EnvHome, home)

	// A helper that exits while holding the lock must not block later writers.
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthFileLockHolderExits$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHelperEnv+"=exit", config.EnvHome+"="+home)
	if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("holder helper exit = %v, output=%s", err, out)
	}

	if err := SetCredential("after-exit", &AuthCredential{AccessToken: "key", AuthMethod: "token"}); err != nil {
		t.Fatalf("SetCredential() after holder exit error = %v", err)
	}
}

// TestAuthFileLockHolderExits exits the process while holding the lock.
func TestAuthFileLockHolderExits(t *testing.T) {
	if os.Getenv(lockHelperEnv) != "exit" {
		t.Skip("helper process only")
	}
	_ = withAuthFileLock(func() error {
		os.Exit(3)
		return nil
	})
}
