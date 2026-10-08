//go:build paused_channels

package mqtt

import (
	"context"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

func TestMQTTTLSConfigVerifiesByDefault(t *testing.T) {
	cfg := mqttTLSConfig(false)
	if cfg.InsecureSkipVerify {
		t.Fatal("the broker certificate is verified unless tls_insecure_skip_verify is set")
	}
	if !mqttTLSConfig(true).InsecureSkipVerify {
		t.Fatal("tls_insecure_skip_verify turns verification off")
	}
}

func TestBrokerUsesTLS(t *testing.T) {
	tests := map[string]bool{
		"ssl://broker:8883":   true,
		"mqtts://broker:8883": true,
		"wss://broker/mqtt":   true,
		"tcp://broker:1883":   false,
		"ws://broker/mqtt":    false,
		"broker:1883":         false,
	}
	for broker, want := range tests {
		if got := brokerUsesTLS(broker); got != want {
			t.Errorf("brokerUsesTLS(%q) = %v, want %v", broker, got, want)
		}
	}
}

type testMessage struct {
	topic   string
	payload []byte
}

func (m testMessage) Duplicate() bool   { return false }
func (m testMessage) Qos() byte         { return 0 }
func (m testMessage) Retained() bool    { return false }
func (m testMessage) Topic() string     { return m.topic }
func (m testMessage) MessageID() uint16 { return 0 }
func (m testMessage) Payload() []byte   { return m.payload }
func (m testMessage) Ack()              {}

func TestHandleInboundAttachesSender(t *testing.T) {
	messageBus := bus.NewMessageBus()
	bc := &config.Channel{
		Type:      config.ChannelMQTT,
		Enabled:   true,
		AllowFrom: config.FlexibleStringSlice{"mqtt:sensor-1"},
	}
	ch, err := NewMQTTChannel(bc, &config.MQTTSettings{Broker: "tcp://localhost:1883", AgentID: "agent"}, messageBus)
	if err != nil {
		t.Fatalf("NewMQTTChannel: %v", err)
	}

	ch.handleInbound(testMessage{topic: "/compa/agent/sensor-2/request", payload: []byte(`{"text":"hi"}`)})
	ch.handleInbound(testMessage{topic: "/compa/agent/sensor-1/request", payload: []byte(`{"text":"hello"}`)})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("expected the listed client's message")
	case inbound := <-messageBus.InboundChan():
		if inbound.Content != "hello" {
			t.Fatalf("content = %q: allow_from must compare the topic's client id", inbound.Content)
		}
		if inbound.Sender.CanonicalID != "mqtt:sensor-1" || inbound.Sender.PlatformID != "sensor-1" {
			t.Fatalf("sender = %+v", inbound.Sender)
		}
		if inbound.ChatID != "mqtt:sensor-1" || inbound.Context.ChatType != "direct" {
			t.Fatalf("chat = %q (%s)", inbound.ChatID, inbound.Context.ChatType)
		}
	}
}
