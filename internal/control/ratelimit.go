package control

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Subscription pulls are the only routes reachable without a session, so they
// are also the only ones an outsider can drive at will. A client refreshing on
// the usual daily-to-hourly cadence never approaches this ceiling; anything
// that does is a misconfigured client or someone probing.
const (
	subscriptionRateWindow = time.Minute
	subscriptionRateLimit  = 30
	// Each unseen key costs a map entry, so the table is capped: rotating
	// source addresses must not grow it without bound.
	subscriptionRateMaxKeys = 4096
)

// rateLimiter counts requests per key in fixed windows.
type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	maxKeys int
	now     func() time.Time
	windows map[string]*rateWindow
}

type rateWindow struct {
	count int
	ends  time.Time
}

func newRateLimiter(window time.Duration, limit, maxKeys int) *rateLimiter {
	return &rateLimiter{
		window: window, limit: limit, maxKeys: maxKeys,
		now: time.Now, windows: map[string]*rateWindow{},
	}
}

// Allow reports whether key may proceed, counting the request against its
// current window.
func (l *rateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.windows) >= l.maxKeys {
		for key, window := range l.windows {
			if !now.Before(window.ends) {
				delete(l.windows, key)
			}
		}
		// Still full means keys arrive faster than windows expire; dropping
		// the table forfeits the current counts but bounds the memory.
		if len(l.windows) >= l.maxKeys {
			l.windows = map[string]*rateWindow{}
		}
	}
	window, seen := l.windows[key]
	if !seen || !now.Before(window.ends) {
		l.windows[key] = &rateWindow{count: 1, ends: now.Add(l.window)}
		return true
	}
	window.count++
	return window.count <= l.limit
}

// throttleSubscription rejects a caller over the limit before the handler
// reaches the database.
func (s *Server) throttleSubscription(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.subscriptionLimiter.Allow(subscriptionThrottleKey(r)) {
			w.Header().Set("Retry-After", strconv.Itoa(int(subscriptionRateWindow.Seconds())))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
			return
		}
		next(w, r)
	}
}

// subscriptionThrottleKey names the caller a request is counted against.
//
// A forwarding header is believed only when the connection itself arrives from
// a loopback or private address, which is where a deployment's own reverse
// proxy sits. Trusting one from a public peer would let any caller reset its
// count by inventing an address; refusing to trust one behind a proxy is just
// as bad, because every peer address is then the proxy's and a single flood
// would exhaust the allowance shared by every legitimate client.
func subscriptionThrottleKey(r *http.Request) string {
	peer := addressIP(r.RemoteAddr)
	if !isPrivatePeer(peer) {
		return peer
	}
	// requestIP falls back to the peer when no header carries a usable
	// address, so an unset header costs nothing here.
	return requestIP(r.RemoteAddr, map[string]string{
		"CF-Connecting-IP": r.Header.Get("CF-Connecting-IP"),
		"X-Real-IP":        r.Header.Get("X-Real-IP"),
		"X-Forwarded-For":  r.Header.Get("X-Forwarded-For"),
	})
}

func isPrivatePeer(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
