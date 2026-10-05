package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewListbuiltinSubcommand(t *testing.T) {
	cmd := newListBuiltinCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "list-builtin", cmd.Use)
	assert.Equal(t, "List available builtin skills", cmd.Short)

	assert.NotNil(t, cmd.RunE)

	assert.True(t, cmd.HasExample())
	assert.False(t, cmd.HasSubCommands())

	assert.False(t, cmd.HasFlags())

	assert.Len(t, cmd.Aliases, 0)
}

// list-builtin and install-builtin use the skills bundled in the binary (EV-27).
func TestBuiltinSkillsComeFromTheBinary(t *testing.T) {
	fsys, err := builtinSkillsFS()
	require.NoError(t, err)
	list, err := listBuiltinSkills(fsys)
	require.NoError(t, err)
	require.NotEmpty(t, list)
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
		assert.NotEmpty(t, s.Description, "skill %s has no description", s.Name)
	}
	assert.Contains(t, names, "tmux")

	var out bytes.Buffer
	require.NoError(t, skillsListBuiltin(&out, fsys))
	assert.Contains(t, out.String(), "tmux")

	workspace := t.TempDir()
	// A skill the user already has is kept.
	mine := filepath.Join(workspace, "skills", "tmux", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(mine), 0o755))
	require.NoError(t, os.WriteFile(mine, []byte("mine"), 0o644))

	out.Reset()
	require.NoError(t, installBuiltinSkills(&out, fsys, workspace))
	got, err := os.ReadFile(mine)
	require.NoError(t, err)
	assert.Equal(t, "mine", string(got))
	assert.True(t, strings.Contains(out.String(), "tmux is already installed"))
	for _, name := range names {
		if name == "tmux" {
			continue
		}
		_, err := os.Stat(filepath.Join(workspace, "skills", name, "SKILL.md"))
		assert.NoError(t, err, "builtin skill %s not installed", name)
	}
}
