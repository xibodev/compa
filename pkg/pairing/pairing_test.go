package pairing

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// setClock pins the package clock for one test.
func setClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	current := at
	now = func() time.Time { return current }
	t.Cleanup(func() { now = time.Now })
	return &current
}

func TestRecordUpsertsBySender(t *testing.T) {
	home := t.TempDir()
	clock := setClock(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	first := *clock

	if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:42", PlatformID: "42", DisplayName: "Ann"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	*clock = first.Add(time.Hour)
	if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:42", DisplayName: "Ann B."}); err != nil {
		t.Fatalf("Record() again error = %v", err)
	}
	if err := Record(home, Request{Channel: "discord", SenderID: "discord:7"}); err != nil {
		t.Fatalf("Record() other channel error = %v", err)
	}

	got, err := List(home, "telegram")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List(telegram) = %+v, want one request", got)
	}
	r := got[0]
	if r.Count != 2 || r.PlatformID != "42" || r.DisplayName != "Ann B." {
		t.Fatalf("request = %+v, want count 2, platform id kept and the newer name", r)
	}
	if !r.FirstSeen.Equal(first) || !r.LastSeen.Equal(first.Add(time.Hour)) {
		t.Fatalf("seen = %v .. %v, want %v .. %v", r.FirstSeen, r.LastSeen, first, first.Add(time.Hour))
	}

	all, err := List(home, "")
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if len(all) != 2 || all[0].Channel != "discord" {
		t.Fatalf("List(all) = %+v, want both channels, most recent first", all)
	}
}

func TestRecordRejectsMissingIdentity(t *testing.T) {
	home := t.TempDir()
	if err := Record(home, Request{Channel: "telegram"}); err == nil {
		t.Fatal("Record() without a sender id succeeded")
	}
	if err := Record(home, Request{SenderID: "telegram:1"}); err == nil {
		t.Fatal("Record() without a channel succeeded")
	}
	if err := Record("", Request{Channel: "telegram", SenderID: "telegram:1"}); err == nil {
		t.Fatal("Record() without a home succeeded")
	}
}

func TestListOfMissingStoreIsEmpty(t *testing.T) {
	got, err := List(t.TempDir(), "telegram")
	if err != nil || len(got) != 0 {
		t.Fatalf("List() = %v, %v; want no requests and no error", got, err)
	}
}

func TestRecordKeepsTheMostRecentSendersPerChannel(t *testing.T) {
	home := t.TempDir()
	clock := setClock(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	start := *clock

	for i := range MaxPendingPerChannel + 5 {
		*clock = start.Add(time.Duration(i) * time.Minute)
		if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("Record(%d) error = %v", i, err)
		}
	}
	if err := Record(home, Request{Channel: "slack", SenderID: "slack:U1"}); err != nil {
		t.Fatalf("Record(slack) error = %v", err)
	}

	got, err := List(home, "telegram")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != MaxPendingPerChannel {
		t.Fatalf("kept %d telegram requests, want %d", len(got), MaxPendingPerChannel)
	}
	if got[0].SenderID != "telegram:24" || got[len(got)-1].SenderID != "telegram:5" {
		t.Fatalf("kept %s .. %s, want the 20 most recent (telegram:24 .. telegram:5)", got[0].SenderID, got[len(got)-1].SenderID)
	}
	if slack, _ := List(home, "slack"); len(slack) != 1 {
		t.Fatalf("the cap of one channel touched another: slack = %+v", slack)
	}
}

func TestRequestsExpireAfterAWeekOfSilence(t *testing.T) {
	home := t.TempDir()
	clock := setClock(t, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	start := *clock

	if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:old"}); err != nil {
		t.Fatalf("Record(old) error = %v", err)
	}
	*clock = start.Add(6 * 24 * time.Hour)
	if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:new"}); err != nil {
		t.Fatalf("Record(new) error = %v", err)
	}

	*clock = start.Add(Expiry + time.Minute)
	got, err := List(home, "telegram")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0].SenderID != "telegram:new" {
		t.Fatalf("List() = %+v, want only the request seen within a week", got)
	}

	// The next write drops the expired request from the file too.
	if err := Record(home, Request{Channel: "discord", SenderID: "discord:1"}); err != nil {
		t.Fatalf("Record(discord) error = %v", err)
	}
	stored, err := load(filepath.Join(home, FileName))
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	for _, r := range stored {
		if r.SenderID == "telegram:old" {
			t.Fatalf("expired request still stored: %+v", stored)
		}
	}
}

func TestRemove(t *testing.T) {
	home := t.TempDir()
	for _, r := range []Request{
		{Channel: "telegram", SenderID: "telegram:1"},
		{Channel: "telegram", SenderID: "telegram:2"},
		{Channel: "discord", SenderID: "telegram:1"},
	} {
		if err := Record(home, r); err != nil {
			t.Fatalf("Record(%+v) error = %v", r, err)
		}
	}

	if err := Remove(home, "telegram", "telegram:1"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := Remove(home, "telegram", "telegram:missing"); err != nil {
		t.Fatalf("Remove() of a missing request error = %v", err)
	}

	telegram, _ := List(home, "telegram")
	if len(telegram) != 1 || telegram[0].SenderID != "telegram:2" {
		t.Fatalf("telegram after Remove = %+v, want only telegram:2", telegram)
	}
	if discord, _ := List(home, "discord"); len(discord) != 1 {
		t.Fatalf("Remove on telegram touched discord: %+v", discord)
	}
}

func TestStoreFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX permission bits on Windows")
	}
	home := t.TempDir()
	if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:1"}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(home, FileName))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("pairing.json mode = %o, want 600", mode)
	}
}

func TestConcurrentRecordsLoseNothing(t *testing.T) {
	home := t.TempDir()
	const writers, perWriter = 8, 10

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				// Every writer counts the shared sender and adds one of its own.
				if err := Record(home, Request{Channel: "telegram", SenderID: "telegram:shared"}); err != nil {
					errs <- err
				}
			}
			if err := Record(home, Request{Channel: "discord", SenderID: fmt.Sprintf("discord:%d", w)}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Record() error = %v", err)
	}

	shared, err := List(home, "telegram")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(shared) != 1 || shared[0].Count != writers*perWriter {
		t.Fatalf("shared request = %+v, want count %d", shared, writers*perWriter)
	}
	if discord, _ := List(home, "discord"); len(discord) != writers {
		t.Fatalf("discord requests = %d, want %d", len(discord), writers)
	}
}

const (
	helperEnv        = "COMPA_PAIRING_LOCK_HELPER"
	helperHomeEnv    = "COMPA_PAIRING_LOCK_HOME"
	helperIterations = 20
	helperProcesses  = 4
)

// TestPairingLockHelperProcess is the body of each helper process started by
// TestRecordSerializesProcesses; it does nothing in a normal run.
func TestPairingLockHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process only")
	}
	for range helperIterations {
		if err := Record(os.Getenv(helperHomeEnv), Request{Channel: "telegram", SenderID: "telegram:shared"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func TestRecordSerializesProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts helper processes")
	}
	home := t.TempDir()
	cmds := make([]*exec.Cmd, 0, helperProcesses)
	for range helperProcesses {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPairingLockHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), helperEnv+"=1", helperHomeEnv+"="+home)
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

	got, err := List(home, "telegram")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0].Count != helperProcesses*helperIterations {
		t.Fatalf("shared request = %+v, want count %d (lost updates across processes)", got, helperProcesses*helperIterations)
	}
}
