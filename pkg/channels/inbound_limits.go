package channels

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Inbound limits. A sender other than the owner may start 20 turns a minute,
// in bursts of up to 10, and at most maxQueuedInbound messages of a channel
// may wait for room on the bus. Messages beyond either are dropped: blocking
// would pile up one goroutine per message in the platforms that handle each
// event on its own goroutine.
var (
	senderRateEvery        = 3 * time.Second
	senderRateBurst        = 10
	maxQueuedInbound int64 = 32
)

// maxTrackedSenders bounds the per-sender limiters kept before idle ones
// are pruned.
const maxTrackedSenders = 1024

// senderLimits keeps one rate limiter per sender.
type senderLimits struct {
	mu       sync.Mutex
	limiters map[string]*senderLimiter
}

type senderLimiter struct {
	limiter *rate.Limiter
	last    time.Time
}

// allow reports whether the sender identified by key may send another
// message now.
func (s *senderLimits) allow(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limiters == nil {
		s.limiters = make(map[string]*senderLimiter)
	}
	l, ok := s.limiters[key]
	if !ok {
		if len(s.limiters) >= maxTrackedSenders {
			// A limiter idle long enough to have refilled loses nothing
			// when it is dropped. Past that, the map stays bounded at the
			// cost of resetting some senders' limits.
			idle := senderRateEvery * time.Duration(senderRateBurst)
			for k, other := range s.limiters {
				if now.Sub(other.last) >= idle {
					delete(s.limiters, k)
				}
			}
			for k := range s.limiters {
				if len(s.limiters) < maxTrackedSenders {
					break
				}
				delete(s.limiters, k)
			}
		}
		l = &senderLimiter{limiter: rate.NewLimiter(rate.Every(senderRateEvery), senderRateBurst)}
		s.limiters[key] = l
	}
	l.last = now
	return l.limiter.AllowN(now, 1)
}
