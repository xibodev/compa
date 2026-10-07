// Package pairing keeps the direct messages that a channel with dm_policy
// "pairing" received from senders not in its allow_from. Such a message is
// not processed; the channel records the sender here, and the owner approves
// it in the dashboard, which appends SenderID to the channel's allow_from.
//
// The requests live in pairing.json in the Compa home. Each channel keeps at
// most MaxPendingPerChannel of them: a new sender beyond that replaces the one
// seen longest ago. A request whose sender has been silent for Expiry is
// dropped.
package pairing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/fileutil"
)

const (
	// FileName is the store's file in the Compa home.
	FileName = "pairing.json"
	// MaxPendingPerChannel bounds the requests kept for one channel.
	MaxPendingPerChannel = 20
	// Expiry is how long a request is kept after its sender's last message.
	Expiry = 7 * 24 * time.Hour

	// maxDisplayNameRunes bounds the sender-chosen name kept with a request.
	maxDisplayNameRunes = 128
)

// Request is one sender waiting for the owner's approval.
type Request struct {
	// Channel is the channel's name in channel_list.
	Channel string `json:"channel"`
	// SenderID is the value to append to the channel's allow_from: the
	// canonical "platform:id" form the allow_from matching accepts.
	SenderID string `json:"sender_id"`
	// PlatformID is the sender's id on the platform, for display.
	PlatformID string `json:"platform_id,omitempty"`
	// DisplayName is the name the sender shows on the platform.
	DisplayName string    `json:"display_name,omitempty"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	// Count is how many messages the sender sent while waiting.
	Count int `json:"count"`
}

type storeFile struct {
	Requests []Request `json:"requests"`
}

// now is the clock; tests replace it.
var now = time.Now

// Record stores r, or updates the request already stored for the same
// Channel and SenderID: its LastSeen moves forward, Count grows by r.Count
// (one when unset), and a non-empty PlatformID or DisplayName replaces the
// stored one. Zero FirstSeen and LastSeen mean now.
func Record(home string, r Request) error {
	r.Channel = strings.TrimSpace(r.Channel)
	r.SenderID = strings.TrimSpace(r.SenderID)
	if r.Channel == "" || r.SenderID == "" {
		return errors.New("pairing: channel and sender id are required")
	}
	r.PlatformID = strings.TrimSpace(r.PlatformID)
	r.DisplayName = truncateRunes(strings.TrimSpace(r.DisplayName), maxDisplayNameRunes)
	current := now()
	if r.LastSeen.IsZero() {
		r.LastSeen = current
	}
	if r.FirstSeen.IsZero() || r.FirstSeen.After(r.LastSeen) {
		r.FirstSeen = r.LastSeen
	}
	if r.Count < 1 {
		r.Count = 1
	}

	return update(home, func(requests []Request) []Request {
		for i := range requests {
			existing := &requests[i]
			if existing.Channel != r.Channel || existing.SenderID != r.SenderID {
				continue
			}
			if r.LastSeen.After(existing.LastSeen) {
				existing.LastSeen = r.LastSeen
			}
			if r.FirstSeen.Before(existing.FirstSeen) {
				existing.FirstSeen = r.FirstSeen
			}
			existing.Count += r.Count
			if r.PlatformID != "" {
				existing.PlatformID = r.PlatformID
			}
			if r.DisplayName != "" {
				existing.DisplayName = r.DisplayName
			}
			return requests
		}
		return capChannel(append(requests, r), r.Channel)
	})
}

// List returns the pending requests of channel, most recently seen first. An
// empty channel lists the requests of every channel.
func List(home, channel string) ([]Request, error) {
	path, err := storePath(home)
	if err != nil {
		return nil, err
	}
	requests, err := load(path)
	if err != nil {
		return nil, err
	}
	channel = strings.TrimSpace(channel)
	cutoff := now().Add(-Expiry)
	out := make([]Request, 0, len(requests))
	for _, r := range requests {
		if channel != "" && r.Channel != channel {
			continue
		}
		if r.LastSeen.Before(cutoff) {
			continue
		}
		out = append(out, r)
	}
	sortRecentFirst(out)
	return out, nil
}

// Remove deletes the request of senderID on channel, as approving or
// rejecting it does. Removing a request that is not stored is not an error.
func Remove(home, channel, senderID string) error {
	channel = strings.TrimSpace(channel)
	senderID = strings.TrimSpace(senderID)
	return update(home, func(requests []Request) []Request {
		kept := requests[:0]
		for _, r := range requests {
			if r.Channel == channel && r.SenderID == senderID {
				continue
			}
			kept = append(kept, r)
		}
		return kept
	})
}

// update applies change to the stored requests under the store's file lock
// and writes the result, without the requests that have expired.
func update(home string, change func([]Request) []Request) error {
	path, err := storePath(home)
	if err != nil {
		return err
	}
	return config.WithFileLock(path, func() error {
		requests, err := load(path)
		if err != nil {
			return err
		}
		requests = change(dropExpired(requests, now()))
		sortRecentFirst(requests)
		data, err := json.MarshalIndent(storeFile{Requests: requests}, "", "  ")
		if err != nil {
			return err
		}
		return fileutil.WriteFileAtomic(path, data, 0o600)
	})
}

func storePath(home string) (string, error) {
	if strings.TrimSpace(home) == "" {
		return "", errors.New("pairing: the Compa home is required")
	}
	return filepath.Join(home, FileName), nil
}

func load(path string) ([]Request, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("pairing: read %s: %w", path, err)
	}
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("pairing: parse %s: %w", path, err)
	}
	return file.Requests, nil
}

func dropExpired(requests []Request, current time.Time) []Request {
	cutoff := current.Add(-Expiry)
	kept := requests[:0]
	for _, r := range requests {
		if r.LastSeen.Before(cutoff) {
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// capChannel keeps the MaxPendingPerChannel most recently seen requests of
// channel, so a burst of strangers cannot grow the store without bound.
func capChannel(requests []Request, channel string) []Request {
	count := 0
	for _, r := range requests {
		if r.Channel == channel {
			count++
		}
	}
	for ; count > MaxPendingPerChannel; count-- {
		stalest := -1
		for i, r := range requests {
			if r.Channel != channel {
				continue
			}
			if stalest < 0 || r.LastSeen.Before(requests[stalest].LastSeen) {
				stalest = i
			}
		}
		requests = append(requests[:stalest], requests[stalest+1:]...)
	}
	return requests
}

func sortRecentFirst(requests []Request) {
	sort.SliceStable(requests, func(i, j int) bool {
		if !requests[i].LastSeen.Equal(requests[j].LastSeen) {
			return requests[i].LastSeen.After(requests[j].LastSeen)
		}
		if requests[i].Channel != requests[j].Channel {
			return requests[i].Channel < requests[j].Channel
		}
		return requests[i].SenderID < requests[j].SenderID
	})
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
