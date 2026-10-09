package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/session"
)

func TestNewAgentCommand(t *testing.T) {
	cmd := NewAgentCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "agent", cmd.Use)
	assert.Equal(t, "Interact with the agent directly", cmd.Short)

	assert.Len(t, cmd.Aliases, 0)
	assert.False(t, cmd.HasSubCommands())

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	assert.True(t, cmd.HasFlags())

	assert.NotNil(t, cmd.Flags().Lookup("debug"))
	assert.NotNil(t, cmd.Flags().Lookup("message"))
	assert.NotNil(t, cmd.Flags().Lookup("session"))
	assert.NotNil(t, cmd.Flags().Lookup("model"))
	assert.NotNil(t, cmd.Flags().Lookup("dir"))
	assert.NotNil(t, cmd.Flags().Lookup("workspace"))
}

func TestCLISessionKey(t *testing.T) {
	defaultKey := cliSessionKey("")
	assert.True(t, session.IsOpaqueSessionKey(defaultKey))
	assert.Equal(t, cliSessionKey("cli:default"), defaultKey)

	named := cliSessionKey("work")
	assert.True(t, session.IsOpaqueSessionKey(named))
	assert.NotEqual(t, defaultKey, named)
	assert.Equal(t, named, cliSessionKey(" work "))

	opaque := session.BuildOpaqueSessionKey("copied-from-ui")
	assert.Equal(t, opaque, cliSessionKey(opaque))
}
