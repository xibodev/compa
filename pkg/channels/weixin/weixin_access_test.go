package weixin

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/channels"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/pairing"
)

func TestHandleInboundMessage_UnpairedSenderMediaIsNotDownloaded(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	var requests atomic.Int32
	messageBus := bus.NewMessageBus()
	ch := &WeixinChannel{
		BaseChannel: channels.NewBaseChannel("weixin", nil, messageBus, []string{"owner"}),
		api: &ApiClient{
			HttpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, http.ErrHandlerTimeout
			})},
		},
		config:      &config.WeixinSettings{CDNBaseURL: "https://cdn.example.com"},
		typingCache: make(map[string]typingTicketCacheEntry),
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	ch.handleInboundMessage(context.Background(), WeixinMessage{
		FromUserID:   "stranger",
		ContextToken: "ctx-token",
		ItemList: []MessageItem{{
			Type: MessageItemTypeImage,
			ImageItem: &ImageItem{
				Media: &CDNMedia{EncryptQueryParam: "param", AesKey: "a2V5a2V5a2V5a2V5a2V5a2V5"},
			},
		}},
	})

	if n := requests.Load(); n != 0 {
		t.Fatalf("made %d requests for an unpaired sender's media", n)
	}
	if _, ok := ch.contextTokens.Load("stranger"); ok {
		t.Error("kept an unpaired sender's reply token")
	}
	select {
	case msg := <-messageBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", msg)
	default:
	}
	recorded, err := pairing.List(home, "weixin")
	if err != nil || len(recorded) != 1 || recorded[0].SenderID != "weixin:stranger" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", recorded, err)
	}
}
