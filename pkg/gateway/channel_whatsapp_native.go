//go:build whatsapp_native

package gateway

// Native WhatsApp links your own WhatsApp account. It links go.mau.fi/libsignal,
// which is GPL-3.0, so only builds made with the whatsapp_native build tag
// include it; see NOTICE.
import _ "github.com/xibodev/compa/v3/pkg/channels/whatsapp_native"
