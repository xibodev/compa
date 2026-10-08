package whatsapp

import (
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
)

func init() {
	channels.RegisterFactory(
		config.ChannelWhatsApp,
		func(channelName, _ string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			settings, _ := decoded.(*config.WhatsAppSettings)
			return NewWhatsAppChannel(bc, channelName, b, StorePath(cfg, settings)), nil
		},
	)
}
