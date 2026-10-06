package cron

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v2/pkg/cron"
)

func TestEnableSubcommand(t *testing.T) {
	open, _ := testStore(t)
	cmd := newEnableCommand(open)

	require.NotNil(t, cmd)

	assert.Equal(t, "enable", cmd.Name())
	assert.Equal(t, "enable <job-id>", cmd.Use)
	assert.Equal(t, "Enable a job", cmd.Short)

	assert.True(t, cmd.HasExample())
	assert.Contains(t, cmd.Example, exampleJobID)
}

func TestEnableReportsEnabledAndFailsForUnknownJob(t *testing.T) {
	open, _ := testStore(t)
	cs, err := open()
	require.NoError(t, err)
	every := int64(60_000)
	job, err := cs.AddJob("water", cron.CronSchedule{Kind: "every", EveryMS: &every}, "m", "", "")
	require.NoError(t, err)
	_, err = cs.SetJobEnabled(job.ID, false)
	require.NoError(t, err)

	out, err := runCommand(newEnableCommand(open), job.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "'water' enabled")

	_, err = runCommand(newEnableCommand(open), "0000000000000000")
	require.Error(t, err)
}
