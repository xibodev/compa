//go:build paused_channels

package vk

import (
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
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
