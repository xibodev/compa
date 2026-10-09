package cron

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/cron"
)

func TestDisableSubcommand(t *testing.T) {
	open, _ := testStore(t)
	cmd := newDisableCommand(open)

	require.NotNil(t, cmd)

	assert.Equal(t, "disable", cmd.Name())
	assert.Equal(t, "disable <job-id>", cmd.Use)
	assert.Equal(t, "Disable a job", cmd.Short)

	assert.True(t, cmd.HasExample())
	assert.Contains(t, cmd.Example, exampleJobID)
}

func TestDisableReportsDisabledAndFailsForUnknownJob(t *testing.T) {
	open, _ := testStore(t)
	cs, err := open()
	require.NoError(t, err)
	every := int64(60_000)
	job, err := cs.AddJob("water", cron.CronSchedule{Kind: "every", EveryMS: &every}, "m", "", "")
	require.NoError(t, err)

	out, err := runCommand(newDisableCommand(open), job.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "'water' disabled")

	_, err = runCommand(newDisableCommand(open), "0000000000000000")
	require.Error(t, err)
}
