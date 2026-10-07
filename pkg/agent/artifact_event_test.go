package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/agent"
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/providers"
)

type mockLLMProvider struct{}

func (m *mockLLMProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:   "I have rendered the video for you.",
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *mockLLMProvider) GetDefaultModel() string {
	return "mock-model"
}

func TestArtifactAndApprovalEventDelivery(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace

	busInst := runtimeevents.NewBus()
	defer busInst.Close()

	ctx := context.Background()
	_, ch, err := busInst.Channel().SubscribeChan(ctx, runtimeevents.SubscribeOptions{
		Name:   "artifact_approval_test",
		Buffer: 32,
	})
	if err != nil {
		t.Fatalf("subscribe to events: %v", err)
	}

	al := agent.NewAgentLoop(
		cfg,
		bus.NewMessageBus(),
		&mockLLMProvider{},
		agent.WithRuntimeEvents(busInst),
	)
	defer al.Close()

	// 1. Emit an authoritative artifact event directly from a tool/operation
	artifactPayload := agent.ArtifactProducedPayload{
		ArtifactID:  "art_video_001",
		Kind:        "video",
		Module:      "render",
		Locator:     "output/explainer.mp4",
		MediaType:   "video/mp4",
		Bytes:       1048576,
		Digest:      "sha256:abcdef1234567890",
		Title:       "Final Explainer Video",
		ReviewState: "verified",
		ToolName:    "video_compose",
	}

	res := busInst.Publish(ctx, runtimeevents.Event{
		ID:   "ev_art_1",
		Kind: runtimeevents.KindArtifactProduced,
		Time: time.Now(),
		Source: runtimeevents.Source{
			Component: "toolbox",
			Name:      "video_compose",
		},
		Payload: artifactPayload,
	})
	if res.Delivered == 0 {
		t.Fatalf("expected artifact event to be delivered, got 0")
	}

	// 2. Emit an approval requested event
	approvalPayload := agent.ApprovalRequestedPayload{
		ApprovalID: "appr_001",
		ActionID:   "publish_render",
		Tool:       "output_review",
		Reason:     "Human verification required before publishing deliverable",
		State:      "pending",
	}

	res = busInst.Publish(ctx, runtimeevents.Event{
		ID:   "ev_appr_1",
		Kind: runtimeevents.KindApprovalRequested,
		Time: time.Now(),
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "turn_guard",
		},
		Payload: approvalPayload,
	})
	if res.Delivered == 0 {
		t.Fatalf("expected approval event to be delivered, got 0")
	}

	// 3. Verify events delivered over channel
	receivedKinds := make(map[runtimeevents.Kind]bool)
	timeout := time.After(2 * time.Second)

collectLoop:
	for len(receivedKinds) < 2 {
		select {
		case ev := <-ch:
			receivedKinds[ev.Kind] = true
			if ev.Kind == runtimeevents.KindArtifactProduced {
				payload, ok := ev.Payload.(agent.ArtifactProducedPayload)
				if !ok {
					t.Errorf("expected ArtifactProducedPayload, got %T", ev.Payload)
				} else if payload.ArtifactID != "art_video_001" {
					t.Errorf("expected artifact ID art_video_001, got %q", payload.ArtifactID)
				}
			}
			if ev.Kind == runtimeevents.KindApprovalRequested {
				payload, ok := ev.Payload.(agent.ApprovalRequestedPayload)
				if !ok {
					t.Errorf("expected ApprovalRequestedPayload, got %T", ev.Payload)
				} else if payload.ApprovalID != "appr_001" {
					t.Errorf("expected approval ID appr_001, got %q", payload.ApprovalID)
				}
			}
		case <-timeout:
			break collectLoop
		}
	}

	if !receivedKinds[runtimeevents.KindArtifactProduced] {
		t.Errorf("did not receive KindArtifactProduced event")
	}
	if !receivedKinds[runtimeevents.KindApprovalRequested] {
		t.Errorf("did not receive KindApprovalRequested event")
	}

	// 4. Resolve approval
	resolvePayload := agent.ApprovalResolvedPayload{
		ApprovalID: "appr_001",
		Resolution: "approved",
		Reason:     "Verified by operator",
	}
	res = busInst.Publish(ctx, runtimeevents.Event{
		ID:      "ev_appr_res_1",
		Kind:    runtimeevents.KindApprovalResolved,
		Time:    time.Now(),
		Payload: resolvePayload,
	})
	if res.Delivered == 0 {
		t.Fatalf("expected approval resolved event to be delivered")
	}

	t.Logf("Artifact and approval event delivery verified successfully")
}
