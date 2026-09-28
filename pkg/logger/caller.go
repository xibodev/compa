package logger

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// callerSelf is this file's path within the module.
const callerSelf = "pkg/logger/caller.go"

// modulePrefix is how the binary records the module's source directory:
// the checkout's absolute path, or, built with -trimpath, the module path,
// each followed by a slash. It is "" when this file's path does not end
// where the module lays it out.
var modulePrefix = func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	file = filepath.ToSlash(file)
	if !strings.HasSuffix(file, "/"+callerSelf) {
		return ""
	}
	return strings.TrimSuffix(file, callerSelf)
}()

// callerPath returns the path a log line names for a source file: relative
// to the module for the module's own files (pkg/agent/instance.go), whether
// or not the binary was built with -trimpath, and module@version/file for a
// dependency's file in the module cache. The machine's directories never
// show.
func callerPath(file string) string {
	file = strings.ReplaceAll(file, `\`, "/")
	if prefix := modulePrefix; prefix != "" && len(file) > len(prefix) && strings.EqualFold(file[:len(prefix)], prefix) {
		return file[len(prefix):]
	}
	if i := strings.LastIndex(file, "/pkg/mod/"); i >= 0 {
		return file[i+len("/pkg/mod/"):]
	}
	return file
}

// marshalCaller is zerolog's caller field: the file as callerPath names it
// and the line.
func marshalCaller(_ uintptr, file string, line int) string {
	return callerPath(file) + ":" + strconv.Itoa(line)
}

// formatCaller renders the caller field in the console: as zerolog does,
// bold and followed by a cyan ">" on a terminal, but without rewriting the
// path relative to the working directory.
func formatCaller(color bool) func(any) string {
	return func(value any) string {
		caller, _ := value.(string)
		if caller == "" {
			return ""
		}
		if color {
			return "\x1b[1m" + caller + "\x1b[0m\x1b[36m >\x1b[0m"
		}
		return caller + " >"
	}
}
