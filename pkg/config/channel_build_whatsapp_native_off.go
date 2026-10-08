//go:build !whatsapp_native

package config

// WhatsAppNativeInBuild reports whether this build includes native WhatsApp:
// it was made with the whatsapp_native build tag.
const WhatsAppNativeInBuild = false
