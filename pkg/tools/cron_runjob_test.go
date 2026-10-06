package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/cron"
)

func TestCronTool_RunJobReportsAFailedTurnAsAnError(t *testing.T) {
	executor := &stubJobExecutor{err: fmt.Errorf("agent failure")}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())

	job := &cron.CronJob{ID: "job-err"}
	job.Payload.Channel = "telegram"
	job.Payload.To = "chat-1"
	job.Payload.Message = "do something"

	result, err := tool.RunJob(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "agent failure") {
		t.Fatalf("RunJob() error = %v, want the turn's failure", err)
	}
	if !strings.Contains(result, "agent failure") {
		t.Fatalf("RunJob() result = %q, want the error message", result)
	}
}

func TestCronTool_RunJobSucceedsForACompletedTurn(t *testing.T) {
	executor := &stubJobExecutor{response: "done"}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())

	job := &cron.CronJob{ID: "job-ok"}
	job.Payload.Channel = "telegram"
	job.Payload.To = "chat-1"
	job.Payload.Message = "do something"

	if result, err := tool.RunJob(context.Background(), job); err != nil || result != "ok" {
		t.Fatalf("RunJob() = %q, %v; want ok", result, err)
	}
}

func TestCronTool_RunJobReportsADisabledCommandAsAnError(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Enabled = false
	tool := newTestCronToolWithConfig(t, cfg)

	job := &cron.CronJob{}
	job.Payload.Channel = "cli"
	job.Payload.To = "direct"
	job.Payload.Command = "df -h"

	if _, err := tool.RunJob(context.Background(), job); err == nil {
		t.Fatal("RunJob() error = nil for a command that could not run")
	}
}
