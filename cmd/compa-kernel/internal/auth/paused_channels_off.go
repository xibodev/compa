//go:build !paused_channels

package auth

import "github.com/spf13/cobra"

// pausedChannelCommands are the commands that set up paused channels: none
// in a build without them.
func pausedChannelCommands() []*cobra.Command { return nil }
