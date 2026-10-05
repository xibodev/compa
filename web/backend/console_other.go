//go:build !windows

package main

// attachParentConsole has nothing to do: only Windows builds the launcher as
// a GUI program without a console.
func attachParentConsole() bool { return false }
