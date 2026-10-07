package devices

import (
	"context"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/constants"
	"github.com/xibodev/compa/v3/pkg/devices/events"
	"github.com/xibodev/compa/v3/pkg/devices/sources"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/state"
)

type Service struct {
	bus     *bus.MessageBus
	state   *state.Manager
	sources []events.EventSource
	enabled bool
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
}

type Config struct {
	Enabled    bool
	MonitorUSB bool // When true, monitor USB hotplug (Linux only)
	// Future: MonitorBluetooth, MonitorPCI, etc.
}

func NewService(cfg Config, stateMgr *state.Manager) *Service {
	s := &Service{
		state:   stateMgr,
		enabled: cfg.Enabled,
		sources: make([]EventSource, 0),
	}

	if cfg.Enabled && cfg.MonitorUSB {
		s.sources = append(s.sources, sources.NewUSBMonitor())
	}

	return s
}

func (s *Service) SetBus(msgBus *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = msgBus
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || len(s.sources) == 0 {
		logger.InfoC("devices", "Device event service disabled or no sources")
		return nil
	}

	s.ctx, s.cancel = context.WithCancel(ctx)

	for _, src := range s.sources {
		eventCh, err := src.Start(s.ctx)
		if err != nil {
			logger.ErrorCF("devices", "Failed to start source", map[string]any{
				"kind":  src.Kind(),
				"error": err.Error(),
			})
			continue
		}
		go s.handleEvents(src.Kind(), eventCh)
		logger.InfoCF("devices", "Device source started", map[string]any{
			"kind": src.Kind(),
		})
	}

	logger.InfoC("devices", "Device event service started")
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}

	for _, src := range s.sources {
		src.Stop()
	}

	logger.InfoC("devices", "Device event service stopped")
}

func (s *Service) handleEvents(kind events.Kind, eventCh <-chan *events.DeviceEvent) {
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		s.sendNotification(ev)
	}
}

func (s *Service) sendNotification(ev *events.DeviceEvent) {
	s.mu.RLock()
	msgBus := s.bus
	s.mu.RUnlock()

	if msgBus == nil || s.state == nil {
		return
	}

	// Device notices are for the owner: they go to the chat the owner last
	// wrote from, never to whoever wrote last.
	platform, chatID := s.state.GetOwnerChat()
	if platform == "" || chatID == "" || constants.IsInternalChannel(platform) {
		logger.DebugCF("devices", "No owner chat known, skipping notification", map[string]any{
			"kind":   ev.Kind,
			"action": ev.Action,
		})
		return
	}

	msg := ev.FormatMessage()
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	if err := msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context: bus.NewOutboundContext(platform, chatID, ""),
		Content: msg,
	}); err != nil {
		logger.WarnCF("devices", "Failed to send device notification", map[string]any{
			"kind":  ev.Kind,
			"to":    platform,
			"error": err.Error(),
		})
		return
	}

	logger.InfoCF("devices", "Device notification sent", map[string]any{
		"kind":   ev.Kind,
		"action": ev.Action,
		"to":     platform,
	})
}
