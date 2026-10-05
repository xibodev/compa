package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSaveStore_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")

	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob("test", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "hello", "cli", "direct")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("cron store has permission %04o, want 0600", perm)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

// setupService builds a service over a store in a per-test temp directory,
// which also holds the store's lock file.
func setupService(t *testing.T, handler JobHandler, opts ...Option) (*CronService, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.json")
	return NewCronService(path, handler, opts...), path
}

func writeStore(t *testing.T, path string, jobs ...CronJob) {
	t.Helper()
	data, err := json.MarshalIndent(CronStore{Version: 1, Jobs: jobs}, "", "  ")
	if err != nil {
		t.Fatalf("marshal store: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCronService_CRUD(t *testing.T) {
	cs, _ := setupService(t, nil)

	// Test AddJob
	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "at", AtMS: &at}, "msg", "ch", "to")
	if err != nil || job.ID == "" {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Test ListJobs
	if len(cs.ListJobs(true)) != 1 {
		t.Error("ListJobs should return 1 job")
	}

	// Test UpdateJob
	job.Name = "UpdatedName"
	err = cs.UpdateJob(job)
	if err != nil || cs.store.Jobs[0].Name != "UpdatedName" {
		t.Error("UpdateJob failed")
	}

	// Test EnableJob
	cs.EnableJob(job.ID, false)
	if cs.store.Jobs[0].Enabled != false || cs.store.Jobs[0].State.NextRunAtMS != nil {
		t.Error("EnableJob(false) failed to clear state")
	}

	// Test RemoveJob
	removed := cs.RemoveJob(job.ID)
	if !removed || len(cs.store.Jobs) != 0 {
		t.Error("RemoveJob failed")
	}
	if _, err := cs.DeleteJob(job.ID); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("DeleteJob(missing) error = %v, want ErrJobNotFound", err)
	}
}

func TestCronService_GetJobReturnsCopy(t *testing.T) {
	cs, _ := setupService(t, nil)

	everyMS := int64(60_000)
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "ch", "to")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected initial next run")
	}
	nextRun := *job.State.NextRunAtMS

	got, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("GetJob should find job")
	}
	got.Name = "mutated"
	got.Payload.Message = "changed"
	if got.Schedule.EveryMS != nil {
		*got.Schedule.EveryMS = 120_000
	}
	if got.State.NextRunAtMS != nil {
		*got.State.NextRunAtMS = time.Now().Add(3 * time.Hour).UnixMilli()
	}

	again, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("GetJob should still find job")
	}
	if again.Name != "Task1" || again.Payload.Message != "msg" {
		t.Fatalf("GetJob should return a copy, got %+v", again)
	}
	if again.Schedule.EveryMS == nil || *again.Schedule.EveryMS != everyMS {
		t.Fatalf("GetJob should not alias schedule pointers, got %+v", again.Schedule)
	}
	if again.State.NextRunAtMS == nil || *again.State.NextRunAtMS != nextRun {
		t.Fatalf("GetJob should not alias state pointers, got %+v", again.State)
	}
}

func TestCronService_UpdateJobRecomputesNextRunOnScheduleOrEnabledChange(t *testing.T) {
	cs, _ := setupService(t, nil)

	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "at", AtMS: &at}, "msg", "ch", "to")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected initial next run")
	}
	initialNextRun := *job.State.NextRunAtMS

	everyMS := int64(120_000)
	job.Schedule = CronSchedule{Kind: "every", EveryMS: &everyMS}
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob schedule failed: %v", err)
	}
	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("updated job not found")
	}
	if updated.State.NextRunAtMS == nil {
		t.Fatal("expected recomputed next run after schedule change")
	}
	if *updated.State.NextRunAtMS == initialNextRun {
		t.Fatalf("next run should be recomputed, still %d", initialNextRun)
	}

	if disabled := cs.EnableJob(job.ID, false); disabled == nil {
		t.Fatal("EnableJob(false) returned nil")
	}
	disabled, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("disabled job not found")
	}
	disabled.Enabled = true
	if err := cs.UpdateJob(disabled); err != nil {
		t.Fatalf("UpdateJob enabled failed: %v", err)
	}
	reenabled, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("reenabled job not found")
	}
	if !reenabled.Enabled || reenabled.State.NextRunAtMS == nil {
		t.Fatalf("expected enabled job with next run, got %+v", reenabled)
	}
}

func TestCronService_UpdateJobPreservesRunStateOnPayloadOnlyChange(t *testing.T) {
	cs, _ := setupService(t, nil)

	everyMS := int64(60_000)
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "ch", "to")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	lastRun := time.Now().Add(-time.Minute).UnixMilli()
	job.State.LastRunAtMS = &lastRun
	job.State.LastStatus = "ok"
	job.State.LastError = "previous"
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected next run before update")
	}
	nextRun := *job.State.NextRunAtMS

	job.Payload.Message = "updated msg"
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}

	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("updated job not found")
	}
	if updated.State.LastRunAtMS == nil || *updated.State.LastRunAtMS != lastRun {
		t.Fatalf("last run changed: %+v", updated.State)
	}
	if updated.State.LastStatus != "ok" || updated.State.LastError != "previous" {
		t.Fatalf("last status changed: %+v", updated.State)
	}
	if updated.State.NextRunAtMS == nil || *updated.State.NextRunAtMS != nextRun {
		t.Fatalf("next run should be preserved: before=%d after=%+v", nextRun, updated.State.NextRunAtMS)
	}
}

// 2. Test Cron Expression Calculation Logic
func TestCronService_ComputeNextRun(t *testing.T) {
	cs, _ := setupService(t, nil)

	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli()

	tests := []struct {
		name     string
		schedule CronSchedule
		wantNil  bool
	}{
		{"Valid Cron", CronSchedule{Kind: "cron", Expr: "0 * * * *"}, false},
		{"Invalid Cron", CronSchedule{Kind: "cron", Expr: "invalid"}, true},
		{"Every MS", CronSchedule{Kind: "every", EveryMS: int64Ptr(5000)}, false},
		{"At Future", CronSchedule{Kind: "at", AtMS: int64Ptr(now + 1000)}, false},
		{"At Past", CronSchedule{Kind: "at", AtMS: int64Ptr(now - 1000)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cs.computeNextRun(&tt.schedule, now)
			if (got == nil) != tt.wantNil {
				t.Errorf("%s: got %v, wantNil %v", tt.name, got, tt.wantNil)
			}
		})
	}
}

// 3. Test Execution Flow
func TestCronService_ExecutionFlow(t *testing.T) {
	var mu sync.Mutex
	executedJobs := make(map[string]bool)

	handler := func(job *CronJob) (string, error) {
		mu.Lock()
		executedJobs[job.ID] = true
		mu.Unlock()
		return "ok", nil
	}

	cs, _ := setupService(t, handler)

	// Start the service
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	// Add a job then runs 100ms from now
	target := time.Now().Add(100 * time.Millisecond).UnixMilli()
	job, err := cs.AddJob("FastJob", CronSchedule{Kind: "at", AtMS: &target}, "", "", "")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	waitFor(t, "job execution", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return executedJobs[job.ID]
	})

	// check that the job is removed after execution (DeleteAfterRun = true)
	waitFor(t, "job removal", func() bool {
		return cs.Status()["jobs"].(int) == 0
	})
}

func TestCronService_PersistenceIntegrity(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "persist_test.json")

	// write a job and persist
	cs1 := NewCronService(tmpFile, nil)
	at := time.Now().Add(24 * time.Hour).UnixMilli()
	if _, err := cs1.AddJob("PersistMe", CronSchedule{Kind: "at", AtMS: &at}, "payload", "ch1", ""); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// check file exists
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Fatal("Store file was not created")
	}

	// reload and check data integrity
	cs2 := NewCronService(tmpFile, nil)
	if err := cs2.Load(); err != nil {
		t.Fatalf("Failed to load store: %v", err)
	}

	jobs := cs2.ListJobs(true)
	if len(jobs) != 1 || jobs[0].Name != "PersistMe" {
		t.Errorf("Data corruption after reload. Got: %+v", jobs)
	}

	// test loading invalid JSON
	if err := os.WriteFile(tmpFile, []byte("{invalid json}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := OpenCronService(tmpFile, nil); err == nil {
		t.Error("Should return error when loading invalid JSON")
	}
}

func TestCronService_ConcurrentAccess(t *testing.T) {
	cs, _ := setupService(t, nil)

	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	var wg sync.WaitGroup
	workers := 10
	iterations := 50

	wg.Add(workers * 2)

	// add jobs concurrently, one chat per worker so no quota is hit
	for i := range workers {
		go func(id int) {
			defer wg.Done()
			for j := range iterations {
				at := time.Now().Add(time.Hour).UnixMilli()
				if _, err := cs.AddJob(fmt.Sprintf("Job-%d-%d", id, j),
					CronSchedule{Kind: "at", AtMS: &at}, "", "test", fmt.Sprint(id)); err != nil {
					t.Errorf("AddJob failed: %v", err)
				}
				time.Sleep(100 * time.Microsecond)
			}
		}(i)
	}

	// read and update jobs concurrently
	for range workers {
		go func() {
			defer wg.Done()
			for j := range iterations {
				jobs := cs.ListJobs(true)
				if len(jobs) > 0 {
					cs.EnableJob(jobs[0].ID, j%2 == 0)
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
	}

	wg.Wait()
	if got := len(cs.ListJobs(true)); got != workers*iterations {
		t.Fatalf("jobs = %d, want %d", got, workers*iterations)
	}
}

func TestDefaultStorePathIsOutsideWorkspace(t *testing.T) {
	home := filepath.Join("home", ".compa")
	want := filepath.Join(home, "state", "cron", "jobs.json")
	if got := DefaultStorePath(home); got != want {
		t.Fatalf("DefaultStorePath() = %q, want %q", got, want)
	}
}

func TestCronService_EnforcesQuotas(t *testing.T) {
	cs, _ := setupService(t, nil)
	every := CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}

	for i := range MaxJobsPerChat {
		if _, err := cs.AddJob(fmt.Sprint(i), every, "m", "telegram", "chat-1"); err != nil {
			t.Fatalf("AddJob #%d failed: %v", i, err)
		}
	}
	_, err := cs.AddJob("one too many", every, "m", "telegram", "chat-1")
	if err == nil || !strings.Contains(err.Error(), "limit 50") {
		t.Fatalf("per-chat quota error = %v", err)
	}
	// Another chat still has room.
	other, err := cs.AddJob("other", every, "m", "telegram", "chat-2")
	if err != nil {
		t.Fatalf("AddJob for another chat failed: %v", err)
	}
	// Moving a job into a full chat is refused as well.
	other.Payload.To = "chat-1"
	if err := cs.UpdateJob(other); err == nil {
		t.Fatal("UpdateJob into a full chat succeeded")
	}

	for i := len(cs.ListJobs(true)); i < MaxJobs; i++ {
		chat := fmt.Sprintf("bulk-%d", i/MaxJobsPerChat)
		if _, err := cs.AddJob(fmt.Sprint(i), every, "m", "telegram", chat); err != nil {
			t.Fatalf("AddJob #%d failed: %v", i, err)
		}
	}
	_, err = cs.AddJob("over global", every, "m", "telegram", "fresh-chat")
	if err == nil || !strings.Contains(err.Error(), "limit 500") {
		t.Fatalf("global quota error = %v", err)
	}
}

func TestCronSchedule_ValidateRejectsBadSchedules(t *testing.T) {
	past := time.Now().Add(-time.Minute).UnixMilli()
	cases := []struct {
		name     string
		schedule CronSchedule
		wantErr  string
	}{
		{"every below minimum", CronSchedule{Kind: "every", EveryMS: int64Ptr(30_000)}, "at least 60 seconds"},
		{"every zero", CronSchedule{Kind: "every", EveryMS: int64Ptr(0)}, "positive"},
		{"invalid expression", CronSchedule{Kind: "cron", Expr: "not a cron"}, "invalid cron expression"},
		{"seconds expression", CronSchedule{Kind: "cron", Expr: "* * * * * *"}, "more often than once a minute"},
		{"every second tag", CronSchedule{Kind: "cron", Expr: "@everysecond"}, "more often than once a minute"},
		{"unknown time zone", CronSchedule{Kind: "cron", Expr: "0 9 * * *", TZ: "Mars/Olympus"}, "invalid time zone"},
		{"one-time in the past", CronSchedule{Kind: "at", AtMS: &past}, "in the past"},
		{"unknown kind", CronSchedule{Kind: "sometimes"}, "unknown schedule kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.schedule.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want %q", err, tc.wantErr)
			}
		})
	}

	for _, ok := range []CronSchedule{
		{Kind: "every", EveryMS: int64Ptr(60_000)},
		{Kind: "cron", Expr: "* * * * *"},
		{Kind: "cron", Expr: "0 9 * * 1-5", TZ: "UTC"},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", ok, err)
		}
	}
}

func TestCronService_AddAndUpdateRejectInvalidSchedules(t *testing.T) {
	cs, path := setupService(t, nil)

	if _, err := cs.AddJob("bad", CronSchedule{Kind: "cron", Expr: "61 * * * *"}, "m", "", ""); err == nil {
		t.Fatal("AddJob accepted an invalid cron expression")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("store written for a rejected job (err=%v)", err)
	}

	job, err := cs.AddJob("good", CronSchedule{Kind: "cron", Expr: "0 9 * * *"}, "m", "", "")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	job.Schedule = CronSchedule{Kind: "every", EveryMS: int64Ptr(1000)}
	if err := cs.UpdateJob(job); err == nil {
		t.Fatal("UpdateJob accepted a 1s interval")
	}
	stored, _ := cs.GetJob(job.ID)
	if stored.Schedule.Kind != "cron" {
		t.Fatalf("rejected update changed the job: %+v", stored.Schedule)
	}
}

func TestCronService_MissedOneTimeJobs(t *testing.T) {
	var ran sync.Map
	cs, path := setupService(t, func(job *CronJob) (string, error) {
		ran.Store(job.ID, true)
		return "ok", nil
	})

	recent := time.Now().Add(-2 * time.Hour).UnixMilli()
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	writeStore(t, path,
		CronJob{
			ID: "recent", Name: "recent", Enabled: true,
			Schedule: CronSchedule{Kind: "at", AtMS: &recent}, DeleteAfterRun: true,
			State: CronJobState{NextRunAtMS: &recent},
		},
		CronJob{
			ID: "old", Name: "old", Enabled: true,
			Schedule: CronSchedule{Kind: "at", AtMS: &old}, DeleteAfterRun: true,
			State: CronJobState{NextRunAtMS: &old},
		},
	)

	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	waitFor(t, "recent missed job to run", func() bool {
		_, ok := ran.Load("recent")
		return ok
	})
	if _, ok := ran.Load("old"); ok {
		t.Fatal("job missed by more than 24h ran")
	}
	old2, ok := cs.GetJob("old")
	if !ok {
		t.Fatal("old missed job was deleted; want it kept disabled")
	}
	if old2.Enabled || old2.State.LastStatus != "missed" || !strings.Contains(old2.State.LastError, "missed") {
		t.Fatalf("old missed job state = enabled:%v %+v", old2.Enabled, old2.State)
	}
	if _, err := cs.SetJobEnabled("old", true); err == nil {
		t.Fatal("re-enabling a passed one-time job succeeded")
	}
}

func TestCronService_EveryCountdownSurvivesRestart(t *testing.T) {
	cs, path := setupService(t, nil)
	next := time.Now().Add(30 * time.Minute).UnixMilli()
	writeStore(t, path, CronJob{
		ID: "every", Name: "every", Enabled: true,
		Schedule: CronSchedule{Kind: "every", EveryMS: int64Ptr(int64(time.Hour / time.Millisecond))},
		State:    CronJobState{NextRunAtMS: &next},
	})

	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	job, _ := cs.GetJob("every")
	if job.State.NextRunAtMS == nil || *job.State.NextRunAtMS != next {
		t.Fatalf("next run = %v, want the stored %d", job.State.NextRunAtMS, next)
	}
}

func TestNextCronTimeHonorsTimeZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("tz database unavailable: %v", err)
	}
	after := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	next, err := nextCronTime("0 9 * * *", after, tokyo)
	if err != nil {
		t.Fatalf("nextCronTime() error = %v", err)
	}
	if got := next.In(tokyo); got.Hour() != 9 || got.Minute() != 0 || got.Day() != 2 {
		t.Fatalf("next run = %v, want 09:00 Tokyo on June 2", got)
	}

	cs, _ := setupService(t, nil)
	job, err := cs.AddJob("tz", CronSchedule{Kind: "cron", Expr: "0 9 * * *", TZ: "Asia/Tokyo"}, "m", "", "")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if got := time.UnixMilli(*job.State.NextRunAtMS).In(tokyo); got.Hour() != 9 {
		t.Fatalf("job next run = %v, want 09:00 Tokyo", got)
	}
}

func TestNextCronTimeAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz database unavailable: %v", err)
	}

	// Spring forward: 02:30 doesn't exist on 2024-03-10; the run moves to
	// the end of the gap instead of being skipped.
	next, err := nextCronTime("30 2 * * *", time.Date(2024, 3, 10, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatalf("nextCronTime() error = %v", err)
	}
	if want := time.Date(2024, 3, 10, 3, 0, 0, 0, ny); !next.Equal(want) {
		t.Fatalf("spring-forward run = %v, want %v", next, want)
	}

	// Fall back: 01:30 happens twice on 2024-11-03; the job runs once.
	first, err := nextCronTime("30 1 * * *", time.Date(2024, 11, 3, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatalf("nextCronTime() error = %v", err)
	}
	second, err := nextCronTime("30 1 * * *", first.Add(5*time.Second), ny)
	if err != nil {
		t.Fatalf("nextCronTime() error = %v", err)
	}
	if got := second.In(ny); got.Day() != 4 || got.Hour() != 1 || got.Minute() != 30 {
		t.Fatalf("after the first 01:30 the next run is %v, want 01:30 on Nov 4", got)
	}
}

func TestCronService_JobTimeoutCancelsRun(t *testing.T) {
	cs, _ := setupService(t, nil, WithJobTimeout(100*time.Millisecond))
	var sawDeadline atomic.Bool
	cs.SetOnJobContext(func(ctx context.Context, job *CronJob) (string, error) {
		<-ctx.Done()
		sawDeadline.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
		return "", ctx.Err()
	})
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	job, err := cs.AddJob("slow", CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}, "m", "", "")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	cs.findJobLocked(job.ID).State.NextRunAtMS = int64Ptr(time.Now().UnixMilli())
	cs.mu.Unlock()
	cs.checkJobs()

	waitFor(t, "timed-out job state", func() bool {
		got, _ := cs.GetJob(job.ID)
		return got.State.LastStatus == "error"
	})
	if !sawDeadline.Load() {
		t.Fatal("job context did not carry the timeout")
	}
	got, _ := cs.GetJob(job.ID)
	if got.State.NextRunAtMS == nil {
		t.Fatal("recurring job lost its next run after a timeout")
	}
}

func TestCronService_StopCancelsJobsAndNeverSaves(t *testing.T) {
	started := make(chan struct{})
	cs, path := setupService(t, nil)
	cs.SetOnJobContext(func(ctx context.Context, job *CronJob) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	job, err := cs.AddJob("long", CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}, "m", "", "")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	cs.findJobLocked(job.ID).State.NextRunAtMS = int64Ptr(time.Now().UnixMilli())
	cs.mu.Unlock()
	cs.checkJobs()
	<-started

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	cs.Stop()
	cs.jobs.Wait() // the job sees the cancellation and returns

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a stopped service wrote the store:\nbefore: %s\nafter: %s", before, after)
	}
	if _, err := cs.AddJob("late", CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}, "m", "", ""); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("AddJob after Stop error = %v, want ErrServiceStopped", err)
	}
}

func TestCronService_CorruptStoreIsBackedUpAndKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	corrupt := []byte(`{"version":1,"jobs":[{"id":"abc","name":"trunc`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cs, err := OpenCronService(path, nil)
	if err == nil {
		t.Fatal("OpenCronService() succeeded on a corrupt store")
	}
	backups, _ := filepath.Glob(path + ".corrupt-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	if data, _ := os.ReadFile(backups[0]); string(data) != string(corrupt) {
		t.Fatalf("backup content = %q", data)
	}

	if _, err := cs.AddJob("new", CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}, "m", "", ""); err == nil {
		t.Fatal("AddJob on a corrupt store succeeded")
	}
	if data, _ := os.ReadFile(path); string(data) != string(corrupt) {
		t.Fatalf("corrupt store was replaced: %q", data)
	}
	if err := cs.Start(); err == nil {
		t.Fatal("Start() succeeded on a corrupt store")
	}
}

func TestCronService_PicksUpChangesFromAnotherProcess(t *testing.T) {
	gateway, path := setupService(t, nil)
	if err := gateway.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer gateway.Stop()

	every := CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}
	if _, err := gateway.AddJob("from-gateway", every, "m", "", ""); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// The CLI works on its own service over the same file.
	cli := NewCronService(path, nil)
	cliJob, err := cli.AddJob("from-cli", every, "m", "", "")
	if err != nil {
		t.Fatalf("CLI AddJob failed: %v", err)
	}

	// The gateway notices on its next tick...
	gateway.checkJobs()
	if _, ok := gateway.GetJob(cliJob.ID); !ok {
		t.Fatal("gateway did not load the job the CLI added")
	}
	// ...and its own next write keeps the CLI's job.
	if _, err := gateway.AddJob("second-gateway", every, "m", "", ""); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	reread := NewCronService(path, nil)
	if got := len(reread.ListJobs(true)); got != 3 {
		t.Fatalf("store has %d jobs, want 3", got)
	}
}
