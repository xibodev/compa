package cron

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJobTimeoutPausesWhileWaitingForApproval(t *testing.T) {
	ctx, cancel := withJobTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Contexts derived from the run's reach its clock too.
	child, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	resume := PauseJobTimeout(child)
	select {
	case <-ctx.Done():
		t.Fatal("the run timed out while waiting for the owner's approval")
	case <-time.After(300 * time.Millisecond):
	}
	resume()
	resume() // a second call changes nothing

	select {
	case <-child.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the job timeout no longer applies after the approval")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatalf("Err() = %v, child %v; want context.DeadlineExceeded", ctx.Err(), child.Err())
	}
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("a run whose timeout can pause reports a fixed deadline")
	}
}

func TestJobTimeoutOverlappingPauses(t *testing.T) {
	ctx, cancel := withJobTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	first := PauseJobTimeout(ctx)
	second := PauseJobTimeout(ctx)
	first()
	select {
	case <-ctx.Done():
		t.Fatal("the clock ran again while one approval was still waiting")
	case <-time.After(200 * time.Millisecond):
	}
	second()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the clock did not run again after the last approval")
	}
}

func TestJobTimeoutPausedRunStillEndsWithItsParent(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	ctx, cancel := withJobTimeout(parent, time.Hour)
	defer cancel()

	resume := PauseJobTimeout(ctx)
	defer resume()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the service did not end a run waiting for approval")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("Err() = %v, want context.Canceled", ctx.Err())
	}
}

func TestPauseJobTimeoutOutsideAJobDoesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	PauseJobTimeout(ctx)()
	if ctx.Err() != nil {
		t.Fatalf("Err() = %v, want nil", ctx.Err())
	}
}

func TestCronService_ApprovalWaitIsNotPartOfTheJobTimeout(t *testing.T) {
	cs, _ := setupService(t, nil, WithJobTimeout(100*time.Millisecond))
	cs.SetOnJobContext(func(ctx context.Context, job *CronJob) (string, error) {
		resume := PauseJobTimeout(ctx)
		time.Sleep(300 * time.Millisecond) // the owner takes a while to answer
		resume()
		return "ok", ctx.Err()
	})
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	job, err := cs.AddJob("approved", CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)}, "m", "", "")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.mu.Lock()
	cs.findJobLocked(job.ID).State.NextRunAtMS = int64Ptr(time.Now().UnixMilli())
	cs.mu.Unlock()
	cs.checkJobs()

	waitFor(t, "job state", func() bool {
		got, _ := cs.GetJob(job.ID)
		return got.State.LastStatus != ""
	})
	if got, _ := cs.GetJob(job.ID); got.State.LastStatus != "ok" {
		t.Fatalf("last status = %q (%s), want ok: the approval wait counted against the timeout",
			got.State.LastStatus, got.State.LastError)
	}
}
