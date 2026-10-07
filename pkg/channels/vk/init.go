package vk

import (
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
)

func init() {
	channels.RegisterFactory(
		config.ChannelVK,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			if bc == nil {
				return nil, channels.ErrSendFailed
			}
			return NewVKChannel(channelName, bc, b)
		},
	)
}
