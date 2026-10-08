//go:build whatsapp_native

package api

// Native WhatsApp's page, which only builds made with the whatsapp_native
// build tag list.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/pairing"
)

func TestHandleGetChannelConfig_ReturnsNativeWhatsAppChats(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()

	if got := getChannelConfigMap(t, configPath, "whatsapp_native")["chats"]; got != "self" {
		t.Fatalf("chats = %#v, want the default self", got)
	}
	if _, ok := getChannelConfigMap(t, configPath, "whatsapp")["chats"]; ok {
		t.Fatal("the bridge variant shows chats, which only the native client reads")
	}

	patchConfig(t, configPath, `{"channel_list":{"whatsapp":{"type":"whatsapp","settings":{"use_native":true,"chats":"all"}}}}`, http.StatusOK)
	if got := getChannelConfigMap(t, configPath, "whatsapp_native")["chats"]; got != "all" {
		t.Fatalf("chats = %#v, want the saved all", got)
	}
}
func TestPairingNativeWhatsAppListsTheWhatsAppChannel(t *testing.T) {
	_, mux, home := pairingTestHandler(t)
	recordPairingRequest(t, home, pairing.Request{Channel: "whatsapp", SenderID: "whatsapp:15550001"})

	rec := pairingRequestJSON(t, mux, http.MethodGet, "/api/channels/whatsapp_native/pairing", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"whatsapp:15550001"`) {
		t.Fatalf("status = %d, body = %s; want the request of channel_list.whatsapp", rec.Code, rec.Body.String())
	}
}
