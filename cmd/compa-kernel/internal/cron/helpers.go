package cron

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/xibodev/compa/v2/pkg/cron"
)

func cronListCmd(out io.Writer, open openStore) error {
	cs, err := open()
	if err != nil {
		return err
	}
	jobs := cs.ListJobs(true) // Show all jobs, including disabled

	if len(jobs) == 0 {
		fmt.Fprintln(out, "No scheduled jobs.")
		return nil
	}

	fmt.Fprintln(out, "\nScheduled Jobs:")
	fmt.Fprintln(out, "----------------")
	for _, job := range jobs {
		var schedule string
		switch {
		case job.Schedule.Kind == "every" && job.Schedule.EveryMS != nil:
			schedule = fmt.Sprintf("every %ds", *job.Schedule.EveryMS/1000)
		case job.Schedule.Kind == "cron":
			schedule = job.Schedule.Expr
			if job.Schedule.TZ != "" {
				schedule += " (" + job.Schedule.TZ + ")"
			}
		default:
			schedule = "one-time"
		}

		nextRun := "-"
		switch {
		case job.State.NextRunAtMS != nil:
			nextRun = time.UnixMilli(*job.State.NextRunAtMS).Format("2006-01-02 15:04")
		case job.Enabled && job.Schedule.Kind == "at" && job.Schedule.AtMS != nil:
			// A one-off that came due while the gateway was down runs when
			// it starts.
			nextRun = time.UnixMilli(*job.Schedule.AtMS).Format("2006-01-02 15:04") + " (overdue)"
		}

		status := "enabled"
		if !job.Enabled {
			status = "disabled"
		}
		if job.State.LastStatus == "missed" {
			status += " (missed)"
		}

		fmt.Fprintf(out, "  %s (%s)\n", job.Name, job.ID)
		fmt.Fprintf(out, "    Schedule: %s\n", schedule)
		fmt.Fprintf(out, "    Status: %s\n", status)
		fmt.Fprintf(out, "    Next run: %s\n", nextRun)
		if job.State.LastError != "" {
			fmt.Fprintf(out, "    Last error: %s\n", job.State.LastError)
		}
	}
	return nil
}

func cronRemoveCmd(out io.Writer, open openStore, jobID string) error {
	cs, err := open()
	if err != nil {
		return err
	}
	if _, err := cs.DeleteJob(jobID); err != nil {
		if errors.Is(err, cron.ErrJobNotFound) {
			return fmt.Errorf("job %s not found", jobID)
		}
		return fmt.Errorf("error removing job %s: %w", jobID, err)
	}
	fmt.Fprintf(out, "✓ Removed job %s\n", jobID)
	return nil
}

func cronSetJobEnabled(out io.Writer, open openStore, jobID string, enabled bool) error {
	cs, err := open()
	if err != nil {
		return err
	}
	job, err := cs.SetJobEnabled(jobID, enabled)
	if err != nil {
		if errors.Is(err, cron.ErrJobNotFound) {
			return fmt.Errorf("job %s not found", jobID)
		}
		return fmt.Errorf("error updating job %s: %w", jobID, err)
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Fprintf(out, "✓ Job '%s' %s\n", job.Name, state)
	return nil
}
