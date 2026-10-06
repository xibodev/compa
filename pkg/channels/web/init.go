package web

import (
	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/channels"
	"github.com/xibodev/compa/v2/pkg/config"
)

func init() {
	channels.RegisterFactory(
		config.ChannelWeb,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			c, ok := decoded.(*config.WebChatSettings)
			if !ok {
				return nil, channels.ErrSendFailed
			}
			ch, err := NewWebChannel(bc, c, b)
			if err != nil {
				return nil, err
			}
			if channelName != config.ChannelWeb {
				ch.SetName(channelName)
			}
			return ch, nil
		},
	)
	channels.RegisterFactory(
		config.ChannelWebClient,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			c, ok := decoded.(*config.WebChatClientSettings)
			if !ok {
				return nil, channels.ErrSendFailed
			}
			ch, err := NewWebClientChannel(bc, c, b)
			if err != nil {
				return nil, err
			}
			if channelName != config.ChannelWebClient {
				ch.SetName(channelName)
			}
			return ch, nil
		},
	)
}
