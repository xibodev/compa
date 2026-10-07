package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/adhocore/gronx"

	"github.com/xibodev/compa/v3/pkg/fileutil"
)

const (
	// MinEveryInterval is the shortest interval a job may repeat at. Every run
	// of an agent-turn job is a model call, so a seconds-level schedule is a
	// cost and flooding risk rather than a feature.
	MinEveryInterval = time.Minute
	// MaxJobsPerChat bounds the jobs one chat (channel and recipient) may hold.
	MaxJobsPerChat = 50
	// MaxJobs bounds the whole store.
	MaxJobs = 500
	// DefaultJobTimeout bounds one run when no timeout is configured.
	DefaultJobTimeout = 10 * time.Minute

	// missedGrace is how late a one-off job still runs after Compa was down.
	missedGrace = 24 * time.Hour
	// maxTimerWait caps one sleep of the scheduler, so it re-reads the wall
	// clock (a long timer can drift across system sleep) and notices store
	// changes made by the CLI at least once a minute.
	maxTimerWait      = time.Minute
	maxConcurrentJobs = 4
)

var (
	// ErrJobNotFound is returned for an unknown job ID.
	ErrJobNotFound = errors.New("job not found")
	// ErrServiceStopped is returned by a service that was stopped: after a
	// reload its successor owns the store, and a late write would undo that
	// successor's changes.
	ErrServiceStopped = errors.New("cron service is stopped")
)

type CronSchedule struct {
	Kind    string `json:"kind"`
	AtMS    *int64 `json:"atMs,omitempty"`
	EveryMS *int64 `json:"everyMs,omitempty"`
	Expr    string `json:"expr,omitempty"`
	TZ      string `json:"tz,omitempty"`
}

type CronPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Command string `json:"command,omitempty"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
}

type CronJobState struct {
	NextRunAtMS *int64 `json:"nextRunAtMs,omitempty"`
	LastRunAtMS *int64 `json:"lastRunAtMs,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

type CronJob struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Enabled        bool         `json:"enabled"`
	Schedule       CronSchedule `json:"schedule"`
	Payload        CronPayload  `json:"payload"`
	State          CronJobState `json:"state"`
	CreatedAtMS    int64        `json:"createdAtMs"`
	UpdatedAtMS    int64        `json:"updatedAtMs"`
	DeleteAfterRun bool         `json:"deleteAfterRun"`
}

type CronStore struct {
	Version int       `json:"version"`
	Jobs    []CronJob `json:"jobs"`
}

type JobHandler func(job *CronJob) (string, error)

// JobHandlerContext runs a job with a context that carries the job timeout
// and is cancelled when the service stops.
type JobHandlerContext func(ctx context.Context, job *CronJob) (string, error)

// Option configures a CronService.
type Option func(*CronService)

// WithJobTimeout bounds one job run; zero or less keeps DefaultJobTimeout.
func WithJobTimeout(d time.Duration) Option {
	return func(cs *CronService) {
		if d > 0 {
			cs.jobTimeout = d
		}
	}
}

// DefaultStorePath is where the cron store lives: <home>/state/cron/jobs.json,
// outside the workspace the agent's file tools can write.
func DefaultStorePath(home string) string {
	return filepath.Join(home, "state", "cron", "jobs.json")
}

type CronService struct {
	storePath  string
	store      *CronStore
	onJob      JobHandler
	onJobCtx   JobHandlerContext
	mu         sync.RWMutex
	running    bool
	stopped    bool
	gen        uint64
	stopChan   chan struct{}
	wakeChan   chan struct{}
	runCtx     context.Context
	cancelRun  context.CancelFunc
	jobs       sync.WaitGroup
	jobSem     chan struct{}
	inFlight   map[string]uint64
	jobTimeout time.Duration
	// loadErr is set while the store on disk can't be read; the service then
	// refuses to write, so a corrupt store is never replaced behind the user's back.
	loadErr error
	// stamp identifies the store file as last read or written, so changes made
	// by another process (the CLI) are noticed and loaded.
	stamp fileStamp
	now   func() time.Time
}

type fileStamp struct {
	exists  bool
	size    int64
	modTime time.Time
}

func NewCronService(storePath string, onJob JobHandler, opts ...Option) *CronService {
	cs := &CronService{
		storePath:  storePath,
		onJob:      onJob,
		wakeChan:   make(chan struct{}, 1),
		jobSem:     make(chan struct{}, maxConcurrentJobs),
		inFlight:   make(map[string]uint64),
		jobTimeout: DefaultJobTimeout,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(cs)
	}
	cs.mu.Lock()
	if err := cs.loadStoreLocked(); err != nil {
		log.Printf("[cron] %v", err)
	}
	cs.mu.Unlock()
	return cs
}

// OpenCronService is NewCronService that reports a store it can't load.
func OpenCronService(storePath string, onJob JobHandler, opts ...Option) (*CronService, error) {
	cs := NewCronService(storePath, onJob, opts...)
	return cs, cs.LoadErr()
}

// LoadErr reports why the store couldn't be loaded, or nil.
func (cs *CronService) LoadErr() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.loadErr
}

func (cs *CronService) Start() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.running {
		return nil
	}

	if err := cs.loadStoreLocked(); err != nil {
		return fmt.Errorf("failed to load store: %w", err)
	}

	cs.stopped = false
	cs.gen++
	cs.inFlight = make(map[string]uint64)
	if err := cs.mutateLocked(func(now time.Time) (bool, error) {
		return cs.prepareJobsLocked(now, true), nil
	}); err != nil {
		return fmt.Errorf("failed to save store: %w", err)
	}

	cs.runCtx, cs.cancelRun = context.WithCancel(context.Background())
	cs.stopChan = make(chan struct{})
	cs.running = true
	go cs.runLoop(cs.stopChan)

	return nil
}

// Stop ends scheduling and cancels running jobs. A stopped service never
// writes the store again.
func (cs *CronService) Stop() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if !cs.running {
		return
	}

	cs.running = false
	cs.stopped = true
	if cs.stopChan != nil {
		close(cs.stopChan)
		cs.stopChan = nil
	}
	if cs.cancelRun != nil {
		cs.cancelRun()
	}
}

func (cs *CronService) runLoop(stopChan chan struct{}) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		cs.mu.RLock()
		nextWake := cs.getNextWakeMS()
		cs.mu.RUnlock()

		delay := maxTimerWait
		if nextWake != nil {
			diff := time.Duration(*nextWake-cs.now().UnixMilli()) * time.Millisecond
			delay = min(max(diff, 0), maxTimerWait)
		}
		timer.Reset(delay)

		select {
		case <-stopChan:
			return
		case <-cs.wakeChan: // wake on new job or update
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			cs.checkJobs()
		}
	}
}

// checkJobs picks up store changes made by other processes and starts the
// jobs that are due.
func (cs *CronService) checkJobs() {
	cs.mu.Lock()
	if !cs.running {
		cs.mu.Unlock()
		return
	}
	gen := cs.gen
	ctx := cs.runCtx
	var due []CronJob
	err := cs.mutateLocked(func(now time.Time) (bool, error) {
		nowMS := now.UnixMilli()
		for i := range cs.store.Jobs {
			job := &cs.store.Jobs[i]
			if !job.Enabled || job.State.NextRunAtMS == nil || *job.State.NextRunAtMS > nowMS {
				continue
			}
			if _, running := cs.inFlight[job.ID]; running {
				continue
			}
			// Clear the next run before the job starts, so it can't be
			// started twice.
			job.State.NextRunAtMS = nil
			cs.inFlight[job.ID] = gen
			due = append(due, cloneCronJob(*job))
		}
		return len(due) > 0, nil
	})
	if err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}
	for range due {
		cs.jobs.Add(1)
	}
	cs.mu.Unlock()

	for _, job := range due {
		go cs.runJob(ctx, gen, job)
	}
}

// runJob runs one job off the scheduler goroutine, bounded by the job timeout
// and cancelled when the service stops.
func (cs *CronService) runJob(ctx context.Context, gen uint64, job CronJob) {
	defer cs.jobs.Done()

	select {
	case cs.jobSem <- struct{}{}:
		defer func() { <-cs.jobSem }()
	case <-ctx.Done():
		cs.finishJob(gen, job.ID, cs.now(), ctx.Err())
		return
	}

	start := cs.now()
	log.Printf("[cron] ▶ executing job '%s' (id: %s, schedule: %s, channel: %s)",
		job.Name, job.ID, job.Schedule.Kind, job.Payload.Channel)

	// Time the run waits for the owner's approval doesn't count against it.
	jobCtx, cancel := withJobTimeout(ctx, cs.jobTimeout)
	err := cs.invoke(jobCtx, &job)
	cancel()
	cs.finishJob(gen, job.ID, start, err)
}

func (cs *CronService) invoke(ctx context.Context, job *CronJob) (err error) {
	cs.mu.RLock()
	handlerCtx, handler := cs.onJobCtx, cs.onJob
	cs.mu.RUnlock()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("job panicked: %v", r)
		}
	}()

	if handlerCtx != nil {
		_, err = handlerCtx(ctx, job)
		return err
	}
	if handler == nil {
		return nil
	}
	// A handler without a context can't be cancelled; stop waiting for it at
	// the deadline so one stuck job can't hold the schedule.
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("job panicked: %v", r)
			}
		}()
		_, herr := handler(job)
		done <- herr
	}()
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("job did not finish: %w", ctx.Err())
	}
}

func (cs *CronService) finishJob(gen uint64, jobID string, start time.Time, runErr error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.inFlight[jobID] == gen {
		delete(cs.inFlight, jobID)
	}
	if !cs.running || cs.gen != gen {
		log.Printf("[cron] job %s finished after the service stopped; its state is not saved", jobID)
		return
	}

	execDuration := cs.now().Sub(start).Milliseconds()
	var name, nextRunStr string
	err := cs.mutateLocked(func(now time.Time) (bool, error) {
		job := cs.findJobLocked(jobID)
		if job == nil {
			log.Printf("[cron] job %s disappeared before state update", jobID)
			return false, nil
		}
		name = job.Name
		startMS := start.UnixMilli()
		job.State.LastRunAtMS = &startMS
		job.UpdatedAtMS = now.UnixMilli()
		if runErr != nil {
			job.State.LastStatus = "error"
			job.State.LastError = runErr.Error()
		} else {
			job.State.LastStatus = "ok"
			job.State.LastError = ""
		}

		switch {
		case job.Schedule.Kind == "at" && job.DeleteAfterRun:
			cs.removeJobLocked(job.ID)
			nextRunStr = "(deleted)"
		case job.Schedule.Kind == "at":
			job.Enabled = false
			job.State.NextRunAtMS = nil
			nextRunStr = "(disabled)"
		case !job.Enabled:
			job.State.NextRunAtMS = nil
			nextRunStr = "(disabled)"
		default:
			job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, now.UnixMilli())
			nextRunStr = "(none)"
			if job.State.NextRunAtMS != nil {
				nextRunStr = time.UnixMilli(*job.State.NextRunAtMS).Format("2006-01-02 15:04:05")
			}
		}
		return true, nil
	})
	if err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}

	if runErr != nil {
		log.Printf("[cron] ✗ job '%s' failed after %dms: %v", name, execDuration, runErr)
	} else {
		log.Printf("[cron] ✓ job '%s' completed in %dms, next run: %s", name, execDuration, nextRunStr)
	}
}

// prepareJobsLocked gives every enabled job its next run after a load. At
// startup a cron job is rescheduled from now (a slot missed while Compa was
// down isn't replayed), while an `every` job keeps its stored countdown. A
// one-off job missed by less than missedGrace runs now; an older one is
// disabled with the reason, rather than staying "scheduled" forever.
func (cs *CronService) prepareJobsLocked(now time.Time, startup bool) bool {
	nowMS := now.UnixMilli()
	changed := false
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if _, running := cs.inFlight[job.ID]; running {
			continue
		}
		if !job.Enabled {
			if job.State.NextRunAtMS != nil {
				job.State.NextRunAtMS = nil
				changed = true
			}
			continue
		}

		var next *int64
		switch job.Schedule.Kind {
		case "at":
			if job.Schedule.AtMS == nil {
				continue
			}
			at := *job.Schedule.AtMS
			if at <= nowMS && nowMS-at >= missedGrace.Milliseconds() {
				job.Enabled = false
				job.State.NextRunAtMS = nil
				job.State.LastStatus = "missed"
				job.State.LastError = fmt.Sprintf("missed: was due at %s while Compa wasn't running",
					time.UnixMilli(at).Format(time.RFC3339))
				job.UpdatedAtMS = nowMS
				log.Printf("[cron] one-time job '%s' (id: %s) was missed and is disabled", job.Name, job.ID)
				changed = true
				continue
			}
			next = &at // in the past means due now
		case "every":
			if job.State.NextRunAtMS != nil {
				continue
			}
			next = cs.computeNextRun(&job.Schedule, nowMS)
		case "cron":
			if !startup && job.State.NextRunAtMS != nil {
				continue
			}
			next = cs.computeNextRun(&job.Schedule, nowMS)
		default:
			continue
		}
		if !sameInt64(next, job.State.NextRunAtMS) {
			job.State.NextRunAtMS = next
			changed = true
		}
	}
	return changed
}

func (cs *CronService) computeNextRun(schedule *CronSchedule, nowMS int64) *int64 {
	switch schedule.Kind {
	case "at":
		if schedule.AtMS != nil && *schedule.AtMS > nowMS {
			return schedule.AtMS
		}
		return nil
	case "every":
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return nil
		}
		next := nowMS + *schedule.EveryMS
		return &next
	case "cron":
		if schedule.Expr == "" {
			return nil
		}
		loc, err := scheduleLocation(schedule.TZ)
		if err != nil {
			log.Printf("[cron] invalid time zone %q: %v", schedule.TZ, err)
			return nil
		}
		nextTime, err := nextCronTime(schedule.Expr, time.UnixMilli(nowMS), loc)
		if err != nil {
			log.Printf("[cron] failed to compute next run for expr '%s': %v", schedule.Expr, err)
			return nil
		}
		nextMS := nextTime.UnixMilli()
		return &nextMS
	default:
		log.Printf("[cron] unknown schedule kind '%s'", schedule.Kind)
		return nil
	}
}

// nextCronTime returns the first run of expr after `after`, reading expr as
// wall-clock time in loc. The expression is matched on the wall clock in UTC,
// which has no DST, and then placed in loc: a slot inside a spring-forward gap
// runs when the gap ends instead of being skipped, and a slot repeated by a
// fall-back runs once.
func nextCronTime(expr string, after time.Time, loc *time.Location) (time.Time, error) {
	local := after.In(loc)
	wall := time.Date(local.Year(), local.Month(), local.Day(),
		local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
	for range 8 {
		next, err := gronx.NextTickAfter(expr, wall, false)
		if err != nil {
			return time.Time{}, err
		}
		t := time.Date(next.Year(), next.Month(), next.Day(), next.Hour(), next.Minute(), next.Second(), 0, loc)
		if t.Hour() != next.Hour() || t.Minute() != next.Minute() {
			// The wall time doesn't exist in loc (spring forward): run at the
			// transition.
			start, end := t.ZoneBounds()
			tWall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
			if tWall.Before(next) {
				t = end
			} else {
				t = start
			}
		}
		if t.After(after) {
			return t, nil
		}
		// The first instance of a repeated wall time already passed.
		wall = next
	}
	return time.Time{}, fmt.Errorf("no run time found for %q", expr)
}

func scheduleLocation(tz string) (*time.Location, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.Local, nil
	}
	return time.LoadLocation(tz)
}

// Validate reports why a schedule can't be used: an unknown kind, a missing
// or past time, an interval below MinEveryInterval, an invalid cron expression
// or time zone, or a cron expression that fires more than once a minute.
func (s CronSchedule) Validate() error {
	if s.TZ != "" {
		if _, err := scheduleLocation(s.TZ); err != nil {
			return fmt.Errorf("invalid time zone %q: %w", s.TZ, err)
		}
	}
	switch s.Kind {
	case "at":
		if s.AtMS == nil {
			return errors.New("one-time schedule needs a time")
		}
		if *s.AtMS <= time.Now().UnixMilli() {
			return errors.New("one-time schedule is in the past")
		}
	case "every":
		if s.EveryMS == nil || *s.EveryMS <= 0 {
			return errors.New("interval must be positive")
		}
		if time.Duration(*s.EveryMS)*time.Millisecond < MinEveryInterval {
			return fmt.Errorf("interval must be at least %d seconds", int(MinEveryInterval.Seconds()))
		}
	case "cron":
		expr := strings.TrimSpace(s.Expr)
		if expr == "" {
			return errors.New("cron expression is empty")
		}
		if !gronx.IsValid(expr) {
			return fmt.Errorf("invalid cron expression %q", s.Expr)
		}
		loc, _ := scheduleLocation(s.TZ)
		prev, err := nextCronTime(expr, time.Now(), loc)
		if err != nil {
			return fmt.Errorf("cron expression %q never runs: %w", s.Expr, err)
		}
		// Seconds-level expressions (six fields, @everysecond) would run
		// more often than the minimum interval allows.
		for range 4 {
			next, err := nextCronTime(expr, prev, loc)
			if err != nil {
				break
			}
			if next.Sub(prev) < MinEveryInterval {
				return fmt.Errorf("cron expression %q runs more often than once a minute", s.Expr)
			}
			prev = next
		}
	default:
		return fmt.Errorf("unknown schedule kind %q", s.Kind)
	}
	return nil
}

// wake up the loop to re-evaluate next wake time immediately (e.g. after add/update/remove jobs)
func (cs *CronService) notify() {
	select {
	case cs.wakeChan <- struct{}{}:
	default:
		// a wake-up is already pending
	}
}

func (cs *CronService) getNextWakeMS() *int64 {
	var nextWake *int64
	for _, job := range cs.store.Jobs {
		if job.Enabled && job.State.NextRunAtMS != nil {
			if nextWake == nil || *job.State.NextRunAtMS < *nextWake {
				nextWake = job.State.NextRunAtMS
			}
		}
	}
	return nextWake
}

func (cs *CronService) Load() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.loadStoreLocked()
}

func (cs *CronService) SetOnJob(handler JobHandler) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJob = handler
}

// SetOnJobContext sets a handler that receives the job's context; it takes
// precedence over SetOnJob.
func (cs *CronService) SetOnJobContext(handler JobHandlerContext) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJobCtx = handler
}

// loadStoreLocked reads the store from disk. A store that can't be read is
// reported and left untouched: a corrupt one is copied to
// jobs.json.corrupt-<time>, and the service refuses to write until the file
// is fixed or removed.
func (cs *CronService) loadStoreLocked() error {
	stamp := statStamp(cs.storePath)
	data, err := os.ReadFile(cs.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			cs.store = &CronStore{Version: 1, Jobs: []CronJob{}}
			cs.loadErr = nil
			cs.stamp = stamp
			return nil
		}
		cs.loadErr = fmt.Errorf("cron store %s can't be read: %w", cs.storePath, err)
		if cs.store == nil {
			cs.store = &CronStore{Version: 1, Jobs: []CronJob{}}
		}
		return cs.loadErr
	}

	store := &CronStore{Version: 1, Jobs: []CronJob{}}
	if err := json.Unmarshal(data, store); err != nil {
		backup := "(backup failed)"
		// Back each distinct corrupt state up once, not on every look.
		if stamp != cs.stamp || cs.loadErr == nil {
			if path, berr := backupCorruptStore(cs.storePath, data); berr == nil {
				backup = path
			} else {
				log.Printf("[cron] failed to back up corrupt store: %v", berr)
			}
		}
		cs.loadErr = fmt.Errorf("cron store %s is corrupt (copy: %s); fix or remove it: %w",
			cs.storePath, backup, err)
		cs.stamp = stamp
		if cs.store == nil {
			cs.store = &CronStore{Version: 1, Jobs: []CronJob{}}
		}
		return cs.loadErr
	}
	if store.Jobs == nil {
		store.Jobs = []CronJob{}
	}
	cs.store = store
	cs.loadErr = nil
	cs.stamp = stamp
	return nil
}

func backupCorruptStore(storePath string, data []byte) (string, error) {
	backup := storePath + ".corrupt-" + time.Now().Format("20060102-150405")
	if _, err := os.Stat(backup); err == nil {
		backup = fmt.Sprintf("%s-%d", backup, time.Now().UnixNano())
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return "", err
	}
	return backup, nil
}

func statStamp(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, size: info.Size(), modTime: info.ModTime()}
}

// withStoreLock runs fn holding the cross-process lock that serializes
// read-modify-write cycles of the store between the gateway and the CLI.
func (cs *CronService) withStoreLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(cs.storePath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(cs.storePath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("failed to lock cron store: %w", err)
	}
	defer func() { _ = unlockFile(f) }()
	return fn()
}

// mutateLocked loads changes another process made to the store, applies fn,
// and saves when fn reports a change, all under the store lock.
func (cs *CronService) mutateLocked(fn func(now time.Time) (bool, error)) error {
	if cs.stopped {
		return ErrServiceStopped
	}
	return cs.withStoreLock(func() error {
		cs.reloadIfChangedLocked()
		if cs.loadErr != nil {
			return cs.loadErr
		}
		changed, err := fn(cs.now())
		if err != nil || !changed {
			return err
		}
		return cs.saveLocked()
	})
}

// reloadIfChangedLocked reads the store again when another process changed
// it. A store that became unreadable keeps the jobs already in memory.
func (cs *CronService) reloadIfChangedLocked() {
	if statStamp(cs.storePath) == cs.stamp {
		return
	}
	previous := cs.store
	if err := cs.loadStoreLocked(); err != nil {
		log.Printf("[cron] %v", err)
		if previous != nil {
			cs.store = previous
		}
		return
	}
	if cs.running {
		cs.prepareJobsLocked(cs.now(), false)
		cs.notify()
	}
}

func (cs *CronService) saveLocked() error {
	if cs.loadErr != nil {
		return cs.loadErr
	}
	if cs.stopped {
		return ErrServiceStopped
	}
	data, err := json.MarshalIndent(cs.store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cs.storePath), 0o700); err != nil {
		return err
	}
	// Use unified atomic write utility with explicit sync for flash storage reliability.
	if err := fileutil.WriteFileAtomic(cs.storePath, data, 0o600); err != nil {
		return err
	}
	cs.stamp = statStamp(cs.storePath)
	return nil
}

func (cs *CronService) findJobLocked(jobID string) *CronJob {
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			return &cs.store.Jobs[i]
		}
	}
	return nil
}

// checkQuotaLocked reports whether one more job may go to the given chat.
func (cs *CronService) checkQuotaLocked(channel, to string) error {
	if len(cs.store.Jobs) >= MaxJobs {
		return fmt.Errorf("there are already %d scheduled jobs (limit %d); remove one first",
			len(cs.store.Jobs), MaxJobs)
	}
	count := 0
	for _, job := range cs.store.Jobs {
		if job.Payload.Channel == channel && job.Payload.To == to {
			count++
		}
	}
	if count >= MaxJobsPerChat {
		return fmt.Errorf("this chat already has %d scheduled jobs (limit %d); remove one first",
			count, MaxJobsPerChat)
	}
	return nil
}

// JobOption sets an optional part of a job AddJob creates.
type JobOption func(*CronPayload)

// WithCommand makes the job run command rather than an agent turn. The job is
// written with it at once, so it never runs, nor stays, without it.
func WithCommand(command string) JobOption {
	return func(p *CronPayload) { p.Command = command }
}

func (cs *CronService) AddJob(
	name string,
	schedule CronSchedule,
	message string,
	channel, to string,
	opts ...JobOption,
) (*CronJob, error) {
	if err := schedule.Validate(); err != nil {
		return nil, err
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	var added CronJob
	err := cs.mutateLocked(func(now time.Time) (bool, error) {
		if err := cs.checkQuotaLocked(channel, to); err != nil {
			return false, err
		}
		nowMS := now.UnixMilli()
		added = CronJob{
			ID:       generateID(),
			Name:     name,
			Enabled:  true,
			Schedule: schedule,
			Payload: CronPayload{
				Kind:    "agent_turn",
				Message: message,
				Channel: channel,
				To:      to,
			},
			State: CronJobState{
				NextRunAtMS: cs.computeNextRun(&schedule, nowMS),
			},
			CreatedAtMS: nowMS,
			UpdatedAtMS: nowMS,
			// One-time tasks (at) should be deleted after execution
			DeleteAfterRun: schedule.Kind == "at",
		}
		for _, opt := range opts {
			opt(&added.Payload)
		}
		cs.store.Jobs = append(cs.store.Jobs, added)
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	cs.notify()
	job := cloneCronJob(added)
	return &job, nil
}

func (cs *CronService) GetJob(jobID string) (*CronJob, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if job := cs.findJobLocked(jobID); job != nil {
		jobCopy := cloneCronJob(*job)
		return &jobCopy, true
	}
	return nil, false
}

func (cs *CronService) UpdateJob(job *CronJob) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	err := cs.mutateLocked(func(now time.Time) (bool, error) {
		current := cs.findJobLocked(job.ID)
		if current == nil {
			return false, ErrJobNotFound
		}
		previous := *current
		updated := cloneCronJob(*job)
		scheduleChanged := !sameSchedule(previous.Schedule, updated.Schedule)
		if scheduleChanged {
			if err := updated.Schedule.Validate(); err != nil {
				return false, err
			}
		}
		if updated.Payload.Channel != previous.Payload.Channel || updated.Payload.To != previous.Payload.To {
			if err := cs.checkQuotaLocked(updated.Payload.Channel, updated.Payload.To); err != nil {
				return false, err
			}
		}
		nowMS := now.UnixMilli()
		updated.UpdatedAtMS = nowMS
		if updated.Enabled {
			if !previous.Enabled || scheduleChanged {
				updated.State.NextRunAtMS = cs.computeNextRun(&updated.Schedule, nowMS)
				if updated.State.NextRunAtMS == nil && updated.Schedule.Kind == "at" {
					return false, errors.New("one-time schedule is in the past")
				}
			}
		} else {
			updated.State.NextRunAtMS = nil
		}
		*current = updated
		return true, nil
	})
	if err != nil {
		return err
	}
	cs.notify()
	return nil
}

func cloneCronJob(job CronJob) CronJob {
	clone := job
	if job.Schedule.AtMS != nil {
		atMS := *job.Schedule.AtMS
		clone.Schedule.AtMS = &atMS
	}
	if job.Schedule.EveryMS != nil {
		everyMS := *job.Schedule.EveryMS
		clone.Schedule.EveryMS = &everyMS
	}
	if job.State.NextRunAtMS != nil {
		nextRunAtMS := *job.State.NextRunAtMS
		clone.State.NextRunAtMS = &nextRunAtMS
	}
	if job.State.LastRunAtMS != nil {
		lastRunAtMS := *job.State.LastRunAtMS
		clone.State.LastRunAtMS = &lastRunAtMS
	}
	return clone
}

func sameSchedule(a, b CronSchedule) bool {
	return a.Kind == b.Kind &&
		sameInt64(a.AtMS, b.AtMS) &&
		sameInt64(a.EveryMS, b.EveryMS) &&
		a.Expr == b.Expr &&
		a.TZ == b.TZ
}

func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (cs *CronService) RemoveJob(jobID string) bool {
	removed, err := cs.DeleteJob(jobID)
	if err != nil && !errors.Is(err, ErrJobNotFound) {
		log.Printf("[cron] failed to remove job %s: %v", jobID, err)
	}
	return removed
}

// DeleteJob is RemoveJob that reports why nothing was removed.
func (cs *CronService) DeleteJob(jobID string) (bool, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	removed := false
	err := cs.mutateLocked(func(time.Time) (bool, error) {
		removed = cs.removeJobLocked(jobID)
		if !removed {
			return false, ErrJobNotFound
		}
		return true, nil
	})
	if err != nil {
		return false, err
	}
	cs.notify()
	return removed, nil
}

func (cs *CronService) removeJobLocked(jobID string) bool {
	before := len(cs.store.Jobs)
	jobs := make([]CronJob, 0, before)
	for _, job := range cs.store.Jobs {
		if job.ID != jobID {
			jobs = append(jobs, job)
		}
	}
	cs.store.Jobs = jobs
	return len(cs.store.Jobs) < before
}

func (cs *CronService) EnableJob(jobID string, enabled bool) *CronJob {
	job, err := cs.SetJobEnabled(jobID, enabled)
	if err != nil {
		if !errors.Is(err, ErrJobNotFound) {
			log.Printf("[cron] failed to update job %s: %v", jobID, err)
		}
		return nil
	}
	return job
}

// SetJobEnabled is EnableJob that reports failures: an unknown job, a
// one-time job whose time has passed, or a store that can't be written.
func (cs *CronService) SetJobEnabled(jobID string, enabled bool) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	var result CronJob
	err := cs.mutateLocked(func(now time.Time) (bool, error) {
		job := cs.findJobLocked(jobID)
		if job == nil {
			return false, ErrJobNotFound
		}
		nowMS := now.UnixMilli()
		if enabled {
			next := cs.computeNextRun(&job.Schedule, nowMS)
			if next == nil && job.Schedule.Kind == "at" {
				return false, errors.New("one-time job's time has passed; give it a new time instead")
			}
			job.State.NextRunAtMS = next
		} else {
			job.State.NextRunAtMS = nil
		}
		job.Enabled = enabled
		job.UpdatedAtMS = nowMS
		result = cloneCronJob(*job)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	cs.notify()
	return &result, nil
}

func (cs *CronService) ListJobs(includeDisabled bool) []CronJob {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	jobs := make([]CronJob, 0, len(cs.store.Jobs))
	for _, job := range cs.store.Jobs {
		if includeDisabled || job.Enabled {
			jobs = append(jobs, cloneCronJob(job))
		}
	}
	return jobs
}

func (cs *CronService) Status() map[string]any {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	return map[string]any{
		"enabled":      cs.running,
		"jobs":         len(cs.store.Jobs),
		"nextWakeAtMS": cs.getNextWakeMS(),
	}
}

func generateID() string {
	// Use crypto/rand for better uniqueness under concurrent access
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
