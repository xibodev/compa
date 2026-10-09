package agent

import (
	"strings"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/routing"
	"github.com/xibodev/compa/v4/pkg/session"
)

// DispatchRequest is the normalized runtime input passed into the agent loop
// after routing and session allocation have completed.
type DispatchRequest struct {
	SessionKey     string
	InboundContext *bus.InboundContext
	RouteResult    *routing.ResolvedRoute
	SessionScope   *session.SessionScope
	UserMessage    string
	Media          []string
}

func (r DispatchRequest) Channel() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.Channel
}

func (r DispatchRequest) ChatID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.ChatID
}

func (r DispatchRequest) MessageID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.MessageID
}

func (r DispatchRequest) ReplyToMessageID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.ReplyToMessageID
}

func (r DispatchRequest) SenderID() string {
	if r.InboundContext == nil {
		return ""
	}
	return r.InboundContext.SenderID
}

func inferChatTypeFromSessionScope(scope *session.SessionScope) string {
	if scope == nil || len(scope.Values) == 0 {
		return ""
	}
	chatValue := strings.TrimSpace(scope.Values["chat"])
	if chatValue == "" {
		return ""
	}
	chatType, _, ok := strings.Cut(chatValue, ":")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(chatType))
}

// SelectedModule is the module the user pointed the agent at for this turn.
//
// Empty when no selection was made, which is the common case: an installed
// module contributes its capability summaries always, and its overlay and
// skills only when someone asks for it.
func (r DispatchRequest) SelectedModule() string {
	if r.InboundContext == nil {
		return ""
	}
	return strings.TrimSpace(r.InboundContext.Raw[bus.MetadataKeySelectedModule])
}

// ModelSelection is the model selection the message chose — an exact target
// or a model route name — or empty when it runs on the agent's model.
func (r DispatchRequest) ModelSelection() string {
	if r.InboundContext == nil {
		return ""
	}
	return strings.TrimSpace(r.InboundContext.Raw[bus.MetadataKeyModelSelection])
}
