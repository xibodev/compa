package gateway

import (
	"runtime"
	"slices"
	"testing"

	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
)

// The gateway registers exactly the channels config.ChannelInBuild says this
// build includes, so the launcher, which lists the channels with
// ChannelInBuild, offers those the kernel beside it runs.
func TestRegisteredChannelsAreThoseInTheBuild(t *testing.T) {
	registered := channels.GetRegisteredFactoryNames()
	for _, typ := range config.ChannelTypes() {
		want := config.ChannelInBuild(typ)
		if typ == config.ChannelMatrix && matrixExcludedPlatform() {
			want = false
		}
		if got := slices.Contains(registered, typ); got != want {
			t.Errorf("channel %s registered = %v, want %v (build tag %q)", typ, got, want, config.ChannelBuildTag(typ))
		}
	}
	for _, name := range registered {
		if !slices.Contains(config.ChannelTypes(), name) {
			t.Errorf("registered channel %s has no config type", name)
		}
	}
}

// matrixExcludedPlatform is whether channel_matrix.go leaves Matrix out of
// this platform's builds.
func matrixExcludedPlatform() bool {
	switch {
	case runtime.GOARCH == "mipsle", runtime.GOOS == "netbsd", runtime.GOOS == "android":
		return true
	}
	return runtime.GOOS == "freebsd" && runtime.GOARCH == "arm"
}
