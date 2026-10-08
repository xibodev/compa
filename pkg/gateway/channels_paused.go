//go:build paused_channels

package gateway

// The paused channels: only builds made with the paused_channels build tag
// include them. Matrix is in channel_matrix.go.
import (
	_ "github.com/xibodev/compa/v3/pkg/channels/deltachat"
	_ "github.com/xibodev/compa/v3/pkg/channels/dingtalk"
	_ "github.com/xibodev/compa/v3/pkg/channels/discord"
	_ "github.com/xibodev/compa/v3/pkg/channels/feishu"
	_ "github.com/xibodev/compa/v3/pkg/channels/irc"
	_ "github.com/xibodev/compa/v3/pkg/channels/line"
	_ "github.com/xibodev/compa/v3/pkg/channels/maixcam"
	_ "github.com/xibodev/compa/v3/pkg/channels/mqtt"
	_ "github.com/xibodev/compa/v3/pkg/channels/onebot"
	_ "github.com/xibodev/compa/v3/pkg/channels/qq"
	_ "github.com/xibodev/compa/v3/pkg/channels/telegram"
	_ "github.com/xibodev/compa/v3/pkg/channels/vk"
	_ "github.com/xibodev/compa/v3/pkg/channels/wecom"
	_ "github.com/xibodev/compa/v3/pkg/channels/weixin"
)
