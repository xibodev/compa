package channels

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/pkg/config"
)

func TestDynamicServeMuxDoesNotLockWhileServing(t *testing.T) {
	dm := newDynamicServeMux()
	entered := make(chan struct{})
	release := make(chan struct{})
	dm.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
	})

	served := make(chan struct{})
	go func() {
		dm.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
		close(served)
	}()
	<-entered

	registered := make(chan struct{})
	go func() {
		dm.HandleFunc("/other", func(http.ResponseWriter, *http.Request) {})
		dm.Unhandle("/other")
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked while a request was being served")
	}
	close(release)
	<-served
}

func TestSharedHTTPServerErrorDoesNotExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// A closed listener makes Serve fail at once; before, that called
	// os.Exit and ended this test binary.
	ln.Close()

	m := newTestManager()
	m.SetupHTTPServerListeners([]net.Listener{ln}, ln.Addr().String(), nil)
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := m.StopAll(context.Background()); err != nil {
		t.Fatalf("StopAll() error = %v", err)
	}
}

func TestToChannelHashesSeesSecretsOfRenamedChannels(t *testing.T) {
	hashFor := func(token string) string {
		t.Helper()
		cfg := &config.Config{Channels: config.ChannelsConfig{}}
		bc := &config.Channel{
			Enabled:  true,
			Type:     config.ChannelTelegram,
			Settings: config.RawNode(`{}`),
		}
		settings := &config.TelegramSettings{}
		if err := bc.Decode(settings); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		settings.Token.Set(token)
		cfg.Channels["my_bot"] = bc
		return toChannelHashes(cfg)["my_bot"]
	}
	if hashFor("token-a") == hashFor("token-b") {
		t.Fatal("a token change on a renamed channel does not change its hash")
	}
}

// Every channel type's secrets count, including those of nested settings
// and the plain secret strings, so a reload restarts a channel whose secret
// alone changed.
func TestToChannelHashesSeeEverySecret(t *testing.T) {
	hashFor := func(channelType string, settings any, set func()) string {
		t.Helper()
		bc := &config.Channel{Enabled: true, Type: channelType, Settings: config.RawNode(`{}`)}
		if err := bc.Decode(settings); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		set()
		cfg := &config.Config{Channels: config.ChannelsConfig{"ch": bc}}
		return toChannelHashes(cfg)["ch"]
	}
	vk := &config.VKSettings{}
	vkA := hashFor(config.ChannelVK, vk, func() { vk.SetToken("vk-a") })
	vkB := hashFor(config.ChannelVK, vk, func() { vk.SetToken("vk-b") })
	if vkA == "" || vkA == vkB {
		t.Error("a VK token change does not change the channel's hash")
	}
	teams := &config.TeamsWebhookSettings{}
	teamsFor := func(url string) string {
		return hashFor(config.ChannelTeamsWebHook, teams, func() {
			teams.Webhooks = map[string]config.TeamsWebhookTarget{"hook": {WebhookURL: *config.NewSecureString(url)}}
		})
	}
	if teamsFor("https://example.com/a") == teamsFor("https://example.com/b") {
		t.Error("a webhook URL change does not change the channel's hash")
	}
}
