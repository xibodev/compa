package cron

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v2/pkg/cron"
)

// testStore opens a store in a temp directory, like the CLI does.
func testStore(t *testing.T) (openStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.json")
	return func() (*cron.CronService, error) { return cron.OpenCronService(path, nil) }, path
}

func runCommand(cmd *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestNewAddSubcommand(t *testing.T) {
	open, _ := testStore(t)
	cmd := newAddCommand(open)

	require.NotNil(t, cmd)

	assert.Equal(t, "add", cmd.Name())
	assert.Contains(t, cmd.Use, "--name <name>")
	assert.Equal(t, "Add a new scheduled job", cmd.Short)

	assert.True(t, cmd.HasFlags())

	assert.NotNil(t, cmd.Flags().Lookup("every"))
	assert.NotNil(t, cmd.Flags().Lookup("cron"))
	assert.NotNil(t, cmd.Flags().Lookup("tz"))
	assert.NotNil(t, cmd.Flags().Lookup("to"))
	assert.NotNil(t, cmd.Flags().Lookup("channel"))

	nameFlag := cmd.Flags().Lookup("name")
	require.NotNil(t, nameFlag)

	messageFlag := cmd.Flags().Lookup("message")
	require.NotNil(t, messageFlag)

	val, found := nameFlag.Annotations[cobra.BashCompOneRequiredFlag]
	require.True(t, found)
	require.NotEmpty(t, val)
	assert.Equal(t, "true", val[0])

	val, found = messageFlag.Annotations[cobra.BashCompOneRequiredFlag]
	require.True(t, found)
	require.NotEmpty(t, val)
	assert.Equal(t, "true", val[0])
}

func TestNewAddCommandEveryAndCronMutuallyExclusive(t *testing.T) {
	open, _ := testStore(t)
	_, err := runCommand(newAddCommand(open),
		"--name", "job",
		"--message", "hello",
		"--every", "10",
		"--cron", "0 9 * * *",
	)
	require.Error(t, err)
}

func TestAddCommandRejectsInvalidSchedules(t *testing.T) {
	cases := map[string][]string{
		"invalid expression": {"--cron", "99 * * * *"},
		"too frequent":       {"--every", "10"},
		"unknown time zone":  {"--cron", "0 9 * * *", "--tz", "Nowhere/Land"},
	}
	for name, schedule := range cases {
		t.Run(name, func(t *testing.T) {
			open, path := testStore(t)
			args := append([]string{"--name", "job", "--message", "hello"}, schedule...)
			_, err := runCommand(newAddCommand(open), args...)
			require.Error(t, err)
			_, statErr := os.Stat(path)
			assert.True(t, os.IsNotExist(statErr), "a rejected job must not be saved")
		})
	}
}

func TestAddCommandAddsJob(t *testing.T) {
	open, _ := testStore(t)
	out, err := runCommand(newAddCommand(open),
		"--name", "standup", "--message", "hello", "--cron", "0 9 * * 1-5")
	require.NoError(t, err)
	assert.Contains(t, out, "Added job 'standup'")

	cs, err := open()
	require.NoError(t, err)
	jobs := cs.ListJobs(true)
	require.Len(t, jobs, 1)
	assert.Equal(t, "0 9 * * 1-5", jobs[0].Schedule.Expr)
}

func TestAddCommandRefusesTimeZoneWithoutCron(t *testing.T) {
	open, path := testStore(t)
	_, err := runCommand(newAddCommand(open),
		"--name", "water", "--message", "hello", "--every", "3600", "--tz", "Europe/Prague")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tz applies only to --cron schedules")
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "a refused job must not be saved")
}

func TestCommandsFailOnCorruptStore(t *testing.T) {
	open, path := testStore(t)
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0o600))

	_, err := runCommand(newListCommand(open))
	require.Error(t, err)

	_, err = runCommand(newAddCommand(open), "--name", "job", "--message", "hello", "--every", "60")
	require.Error(t, err)

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "{broken", string(data), "the corrupt store must not be replaced")
}

func TestListShowsMissedJobs(t *testing.T) {
	open, _ := testStore(t)
	cs, err := open()
	require.NoError(t, err)
	every := int64(60_000)
	job, err := cs.AddJob("water", cron.CronSchedule{Kind: "every", EveryMS: &every}, "m", "", "")
	require.NoError(t, err)
	job.Enabled = false
	job.State.LastStatus = "missed"
	job.State.LastError = "missed: was due while Compa wasn't running"
	require.NoError(t, cs.UpdateJob(job))

	out, err := runCommand(newListCommand(open))
	require.NoError(t, err)
	assert.Contains(t, out, "disabled (missed)")
	assert.True(t, strings.Contains(out, "Last error: missed"), out)
}
