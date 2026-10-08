package channels

import (
	"context"
	"errors"
	"strings"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/media"
)

// errNoMediaStore is why an attachment is unavailable on a channel the manager
// gave no media store, as in tests.
var errNoMediaStore = errors.New("no media store")

// Attachment is one file of an inbound message. Receive downloads it only
// once the message is admitted.
type Attachment struct {
	// Kind is "image", "voice", "audio", "video" or "file".
	Kind string
	// Name is the file's name, when the platform gives one.
	Name string
	// ContentType is the file's MIME type, when known.
	ContentType string
	// Download saves the file locally and returns its path.
	Download func(ctx context.Context) (string, error)
}

// Receive is how a channel hands over an inbound message: it applies the
// access check, then downloads the attachments of an admitted message,
// stores each and adds one note per file to the text, in order, and
// publishes the message. A file that fails to download leaves an
// "[attachment unavailable: ...]" note in its place.
func (c *BaseChannel) Receive(
	ctx context.Context,
	chatID, text string,
	attachments []Attachment,
	inbound bus.InboundContext,
	sender bus.SenderInfo,
) error {
	if !inbound.SenderIsOwner {
		decision, owner := c.admission(inbound.ChatType, sender, strings.TrimSpace(inbound.SenderID))
		switch decision {
		case admitPair:
			c.recordPairingRequest(sender, strings.TrimSpace(inbound.SenderID))
			return nil
		case admitDrop:
			return nil
		}
		inbound.SenderIsOwner = owner
	}

	scope := BuildMediaScope(c.name, chatID, inbound.MessageID)
	notes := make([]string, 0, len(attachments))
	refs := make([]string, 0, len(attachments))
	for _, a := range attachments {
		ref, err := c.storeAttachment(ctx, a, scope)
		if err != nil {
			logger.WarnCF("channels", "Failed to receive an attachment", map[string]any{
				"channel": c.name,
				"kind":    a.Kind,
				"error":   err.Error(),
			})
			notes = append(notes, "[attachment unavailable: "+attachmentLabel(a)+"]")
			continue
		}
		notes = append(notes, attachmentNote(a))
		refs = append(refs, ref)
	}

	content := strings.TrimSpace(strings.Join(append([]string{strings.TrimSpace(text)}, notes...), "\n"))
	if content == "" {
		return nil
	}
	return c.HandleMessageWithContext(ctx, chatID, content, refs, inbound, sender)
}

func (c *BaseChannel) storeAttachment(ctx context.Context, a Attachment, scope string) (string, error) {
	if c.mediaStore == nil {
		return "", errNoMediaStore
	}
	path, err := a.Download(ctx)
	if err != nil {
		return "", err
	}
	return c.mediaStore.Store(path, media.MediaMeta{
		Filename:    a.Name,
		ContentType: a.ContentType,
		Source:      c.name,
	}, scope)
}

// attachmentNote is the note the agent replaces with the file's path: the
// same placeholders it matches, in order (see pkg/agent/agent_media.go).
func attachmentNote(a Attachment) string {
	switch {
	case a.Kind == "voice":
		return "[voice]"
	case a.Name != "":
		return "[" + attachmentKind(a.Kind) + ": " + a.Name + "]"
	default:
		return "[" + attachmentKind(a.Kind) + "]"
	}
}

func attachmentKind(kind string) string {
	switch kind {
	case "image", "audio", "video":
		return kind
	case "voice":
		return "audio"
	default:
		return "file"
	}
}

func attachmentLabel(a Attachment) string {
	if a.Name != "" {
		return a.Name
	}
	if a.Kind == "voice" {
		return "voice message"
	}
	return attachmentKind(a.Kind)
}
