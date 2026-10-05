package onboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOnboardCommand(t *testing.T) {
	cmd := NewOnboardCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "onboard", cmd.Use)
	assert.Equal(t, "Initialize Compa configuration and workspace", cmd.Short)

	assert.Len(t, cmd.Aliases, 1)
	assert.True(t, cmd.HasAlias("o"))

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	assert.True(t, cmd.HasFlags())
	forceFlag := cmd.Flags().Lookup("force")
	require.NotNil(t, forceFlag, "expected --force flag to be registered")
	assert.Equal(t, "false", forceFlag.DefValue, "--force should default to false")
	assert.False(t, cmd.HasSubCommands())
}
