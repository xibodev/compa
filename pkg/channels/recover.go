package channels

import (
	"fmt"
	"runtime/debug"

	"github.com/xibodev/compa/v3/pkg/logger"
)

// RecoverPanic logs a panic raised while a channel handles one event, so a
// malformed event drops that event instead of taking down the whole kernel.
// Use it deferred at the top of every goroutine or callback a platform SDK
// runs for an inbound event:
//
//	defer channels.RecoverPanic("wecom", "message")
func RecoverPanic(channel, where string) {
	if r := recover(); r != nil {
		logger.ErrorCF("channels", "Recovered from a panic while handling an event", map[string]any{
			"channel": channel,
			"where":   where,
			"panic":   fmt.Sprint(r),
			"stack":   string(debug.Stack()),
		})
	}
}
