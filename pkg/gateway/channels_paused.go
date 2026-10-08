//go:build paused_channels

package gateway

// The paused channels, which only builds made with the paused_channels build
// tag include: they get no fixes for now. Matrix, also paused, is in
// channel_matrix.go; native WhatsApp, in channel_whatsapp_native.go.
import (
	_ "github.com/xibodev/compa/v3/pkg/channels/deltachat"
	_ "github.com/xibodev/compa/v3/pkg/channels/dingtalk"
	_ "github.com/xibodev/compa/v3/pkg/channels/feishu"
	_ "github.com/xibodev/compa/v3/pkg/channels/irc"
	_ "github.com/xibodev/compa/v3/pkg/channels/line"
	_ "github.com/xibodev/compa/v3/pkg/channels/maixcam"
	_ "github.com/xibodev/compa/v3/pkg/channels/mqtt"
	_ "github.com/xibodev/compa/v3/pkg/channels/onebot"
	_ "github.com/xibodev/compa/v3/pkg/channels/qq"
	_ "github.com/xibodev/compa/v3/pkg/channels/vk"
	_ "github.com/xibodev/compa/v3/pkg/channels/wecom"
	_ "github.com/xibodev/compa/v3/pkg/channels/weixin"
)
