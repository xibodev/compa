package channels

import (
	"context"
	"testing"
)

func TestCheckWebSocketURL(t *testing.T) {
	ctx := context.Background()
	for _, ok := range []string{
		"ws://127.0.0.1:3001",
		"ws://localhost:3001/ws",
		"ws://192.168.1.20:3001",
		"ws://100.101.102.103:3001", // a Tailscale address
		"wss://onebot.example.com/ws",
	} {
		if err := CheckWebSocketURL(ctx, ok, "test url"); err != nil {
			t.Errorf("%s is refused: %v", ok, err)
		}
	}
	for _, refused := range []string{
		"ws://8.8.8.8:3001",
		"http://127.0.0.1:3001",
	} {
		if err := CheckWebSocketURL(ctx, refused, "test url"); err == nil {
			t.Errorf("%s is accepted", refused)
		}
	}
}
