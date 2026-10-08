package config

import (
	"fmt"
	"runtime"
)

// Build-time variables injected via ldflags during build process.
// These are set by the Makefile or .goreleaser.yaml using the -X flag:
//
//	-X github.com/xibodev/compa/v3/pkg/config.Version=<version>
//	-X github.com/xibodev/compa/v3/pkg/config.GitCommit=<commit>
//	-X github.com/xibodev/compa/v3/pkg/config.BuildTime=<timestamp>
//	-X github.com/xibodev/compa/v3/pkg/config.GoVersion=<go-version>
var (
	Version   = "dev" // Default value when not built with ldflags
	GitCommit string  // Git commit SHA (short)
	BuildTime string  // Build timestamp in RFC3339 format
	GoVersion string  // Go version used for building
)

// GatewayProtocol is the version of the interface docs/gateway.md describes:
// the gateway's HTTP routes, web chat frames and pid file. It goes up only
// when a change can break a program written against that page; additions,
// such as a new route, frame or field, leave it as it is.
const GatewayProtocol = 1

// FormatVersion returns the version string with optional git commit
func FormatVersion() string {
	v := Version
	if GitCommit != "" {
		v += fmt.Sprintf(" (git: %s)", GitCommit)
	}
	return v
}

// FormatBuildInfo returns build time and go version info
func FormatBuildInfo() (string, string) {
	build := BuildTime
	goVer := GoVersion
	if goVer == "" {
		goVer = runtime.Version()
	}
	return build, goVer
}

// GetVersion returns the version string
func GetVersion() string {
	return Version
}
