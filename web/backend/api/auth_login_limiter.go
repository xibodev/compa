package api

import (
	"container/list"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/web/backend/middleware"
)

const (
	loginAttemptsPerIP = 10
	loginAttemptWindow = time.Minute
	// loginAttemptsGlobal bounds attempts from all clients together, so
	// many addresses cannot each try their ten.
	loginAttemptsGlobal = 100
	// loginLimiterMaxClients bounds the clients tracked; the one seen least
	// recently is forgotten first.
	loginLimiterMaxClients = 1024
	logoutBodyMaxBytes     = 4096
)

// loginRateLimiter limits POST /api/auth/login attempts per IP per minute,
// and all attempts together.
type loginRateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*list.Element // of *loginClient
	order   *list.List               // most recently seen first
	global  []time.Time
}

type loginClient struct {
	ip       string
	attempts []time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		now:     time.Now,
		clients: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// allow reserves a slot for this request; false means rate limit exceeded.
func (l *loginRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-loginAttemptWindow)

	l.global = attemptsAfter(l.global, cutoff)
	client := l.client(ip)
	client.attempts = attemptsAfter(client.attempts, cutoff)
	if len(client.attempts) >= loginAttemptsPerIP || len(l.global) >= loginAttemptsGlobal {
		return false
	}
	client.attempts = append(client.attempts, now)
	l.global = append(l.global, now)
	return true
}

// client returns ip's entry, created when new, as the most recently seen.
func (l *loginRateLimiter) client(ip string) *loginClient {
	if element, ok := l.clients[ip]; ok {
		l.order.MoveToFront(element)
		return element.Value.(*loginClient)
	}
	client := &loginClient{ip: ip}
	l.clients[ip] = l.order.PushFront(client)
	for l.order.Len() > loginLimiterMaxClients {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.clients, oldest.Value.(*loginClient).ip)
	}
	return client
}

// attemptsAfter drops the attempts at or before cutoff, which sorted order
// keeps at the front.
func attemptsAfter(attempts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(attempts) && !attempts[i].After(cutoff) {
		i++
	}
	if i == 0 {
		return attempts
	}
	return append(attempts[:0], attempts[i:]...)
}

// clientIPForLimiter is the client address login attempts are counted
// against: behind a trusted proxy, the forwarded client's, not the proxy's
// one address for everyone.
func clientIPForLimiter(r *http.Request, resolver *middleware.ClientIPResolver) string {
	if resolver != nil {
		return resolver.ClientIP(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
