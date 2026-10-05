package cron

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewListSubcommand(t *testing.T) {
	open, _ := testStore(t)
	cmd := newListCommand(open)

	require.NotNil(t, cmd)

	assert.Equal(t, "List all scheduled jobs", cmd.Short)

	out, err := runCommand(cmd)
	require.NoError(t, err)
	assert.Contains(t, out, "No scheduled jobs.")
}
