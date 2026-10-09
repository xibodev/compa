package cron

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/cron"
)

func TestNewRemoveSubcommand(t *testing.T) {
	open, _ := testStore(t)
	cmd := newRemoveCommand(open)

	require.NotNil(t, cmd)

	assert.Equal(t, "remove", cmd.Name())
	assert.Equal(t, "remove <job-id>", cmd.Use)
	assert.Equal(t, "Remove a job by ID", cmd.Short)

	assert.True(t, cmd.HasExample())
	assert.Contains(t, cmd.Example, exampleJobID)
}

func TestRemoveFailsForUnknownJob(t *testing.T) {
	open, _ := testStore(t)
	cs, err := open()
	require.NoError(t, err)
	every := int64(60_000)
	job, err := cs.AddJob("water", cron.CronSchedule{Kind: "every", EveryMS: &every}, "m", "", "")
	require.NoError(t, err)

	out, err := runCommand(newRemoveCommand(open), job.ID)
	require.NoError(t, err)
	assert.Contains(t, out, "Removed job "+job.ID)

	_, err = runCommand(newRemoveCommand(open), job.ID)
	require.Error(t, err)
}
