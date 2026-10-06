package vk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SevereCloud/vksdk/v3/api"
	"github.com/SevereCloud/vksdk/v3/object"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/channels"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/pairing"
)

func TestIsMentioned(t *testing.T) {
	ch := &VKChannel{}
	ch.setGroupID(123)

	tests := []struct {
		name string
		msg  object.MessagesMessage
		want bool
	}{
		{"mention markup", object.MessagesMessage{Text: "[club123|Compa] hi"}, true},
		{"public page markup", object.MessagesMessage{Text: "[public123|Compa], hi"}, true},
		{"typed mention", object.MessagesMessage{Text: "@club123 hi"}, true},
		{"another community", object.MessagesMessage{Text: "[club1234|Other] hi"}, false},
		{"no mention", object.MessagesMessage{Text: "hi all"}, false},
		{"reply to the bot", object.MessagesMessage{Text: "and?", ReplyMessage: &object.MessagesMessage{FromID: -123}}, true},
		{"reply to someone else", object.MessagesMessage{Text: "and?", ReplyMessage: &object.MessagesMessage{FromID: 42}}, false},
	}
	for _, tt := range tests {
		if got := ch.isMentioned(tt.msg); got != tt.want {
			t.Errorf("%s: isMentioned(%q) = %v, want %v", tt.name, tt.msg.Text, got, tt.want)
		}
	}
	if got := ch.stripBotMention("[club123|Compa], what's up?"); got != "what's up?" {
		t.Errorf("stripBotMention = %q", got)
	}
	if (&VKChannel{}).isMentioned(object.MessagesMessage{Text: "[club123|Compa]"}) {
		t.Error("without a group ID nothing is a mention")
	}
}

func TestHandleMessage_UnpairedSenderIsNotLookedUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"response":[{"id":7,"first_name":"Ann","last_name":"Lee"}]}`))
	}))
	defer server.Close()

	messageBus := bus.NewMessageBus()
	vk := api.NewVK("token")
	vk.MethodURL = server.URL + "/"
	ch := &VKChannel{
		BaseChannel: channels.NewBaseChannel("vk", nil, messageBus, []string{"7"}),
		vk:          vk,
		bc:          &config.Channel{},
		ctx:         context.Background(),
	}
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)

	ch.handleMessage(object.MessagesMessage{PeerID: 99, FromID: 99, Text: "hello"})
	if n := lookups.Load(); n != 0 {
		t.Fatalf("looked up an unpaired sender %d times", n)
	}
	requests, err := pairing.List(home, "vk")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "vk:99" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}

	ch.handleMessage(object.MessagesMessage{PeerID: 7, FromID: 7, Text: "hello"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case msg := <-messageBus.InboundChan():
		if msg.Content != "hello" || msg.Context.ChatType != "direct" || msg.Sender.DisplayName != "Ann Lee" {
			t.Fatalf("published %q, chat type %q, sender %+v", msg.Content, msg.Context.ChatType, msg.Sender)
		}
	case <-ctx.Done():
		t.Fatal("the owner's message was not published")
	}
}

func TestClassifySendError(t *testing.T) {
	tests := []struct {
		err  error
		want error
	}{
		{&api.Error{Code: api.ErrTooMany}, channels.ErrRateLimit},
		{&api.Error{Code: api.ErrFlood}, channels.ErrRateLimit},
		{&api.Error{Code: api.ErrServer}, channels.ErrTemporary},
		{&api.Error{Code: api.ErrAccess}, channels.ErrSendFailed},
	}
	for _, tt := range tests {
		if got := classifySendError(tt.err); !errors.Is(got, tt.want) {
			t.Errorf("classifySendError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
