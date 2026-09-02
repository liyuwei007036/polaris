package control

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterCountsPerKeyWithinWindow(t *testing.T) {
	limiter := newRateLimiter(time.Minute, 3, 16)
	at := time.Now()
	limiter.now = func() time.Time { return at }

	for attempt := 1; attempt <= 3; attempt++ {
		if !limiter.Allow("198.51.100.7") {
			t.Fatalf("attempt %d is within the limit but was rejected", attempt)
		}
	}
	if limiter.Allow("198.51.100.7") {
		t.Fatal("an attempt past the limit was allowed")
	}
	if !limiter.Allow("203.0.113.9") {
		t.Fatal("a different caller must not inherit another caller's count")
	}
	at = at.Add(time.Minute)
	if !limiter.Allow("198.51.100.7") {
		t.Fatal("the count must reset once the window closes")
	}
}

func TestRateLimiterBoundsItsKeyTable(t *testing.T) {
	limiter := newRateLimiter(time.Minute, 1, 4)
	at := time.Now()
	limiter.now = func() time.Time { return at }
	for address := 0; address < 200; address++ {
		limiter.Allow(fmt.Sprintf("198.51.100.%d", address))
	}
	if len(limiter.windows) > 4 {
		t.Fatalf("key table holds %d entries, past the cap of 4", len(limiter.windows))
	}
}

func newThrottleTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	server.subscriptionLimiter = newRateLimiter(time.Minute, 1, 16)
	return server
}

// Both subscription routes answer without a session, so both have to be
// throttled; an unthrottled one still costs a query and a log row per hit.
func TestSubscriptionRoutesAreThrottled(t *testing.T) {
	for _, path := range []string{
		"/api/v1/mihomo/subscriptions/unknown-token",
		"/api/v1/subscriptions/access/unknown-token",
	} {
		t.Run(path, func(t *testing.T) {
			handler := newThrottleTestServer(t).Handler()

			first := httptest.NewRecorder()
			handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, path, nil))
			if first.Code != http.StatusNotFound {
				t.Fatalf("first pull returned %d, want 404 for an unknown token", first.Code)
			}
			second := httptest.NewRecorder()
			handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, path, nil))
			if second.Code != http.StatusTooManyRequests {
				t.Fatalf("second pull returned %d, want 429", second.Code)
			}
			if second.Header().Get("Retry-After") == "" {
				t.Fatal("a throttled response must tell the client when to retry")
			}
		})
	}
}

// A caller connecting straight from the public internet has no proxy in front
// of it, so its forwarding headers are its own invention. Honouring them would
// let it reset its count at will.
func TestSubscriptionThrottleIgnoresForwardingHeadersFromPublicPeer(t *testing.T) {
	handler := newThrottleTestServer(t).Handler()
	forged := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}
	statuses := make([]int, 0, len(forged))
	for _, address := range forged {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/mihomo/subscriptions/unknown-token", nil)
		request.RemoteAddr = "198.51.100.7:44321"
		request.Header.Set("X-Forwarded-For", address)
		request.Header.Set("X-Real-IP", address)
		request.Header.Set("CF-Connecting-IP", address)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		statuses = append(statuses, recorder.Code)
	}
	if statuses[0] != http.StatusNotFound {
		t.Fatalf("first pull returned %d, want 404", statuses[0])
	}
	for index, status := range statuses[1:] {
		if status != http.StatusTooManyRequests {
			t.Fatalf("pull %d returned %d despite a forged source address, want 429", index+2, status)
		}
	}
}

// Behind a reverse proxy every peer address is the proxy's own, so the count
// has to follow the address the proxy reports. Sharing one allowance across
// every client would let a single flood lock all of them out.
func TestSubscriptionThrottleSeparatesClientsBehindPrivatePeer(t *testing.T) {
	handler := newThrottleTestServer(t).Handler()
	pull := func(clientIP string) int {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/mihomo/subscriptions/unknown-token", nil)
		request.RemoteAddr = "127.0.0.1:41234"
		request.Header.Set("X-Real-IP", clientIP)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	if status := pull("198.51.100.7"); status != http.StatusNotFound {
		t.Fatalf("first client returned %d, want 404", status)
	}
	if status := pull("203.0.113.9"); status != http.StatusNotFound {
		t.Fatalf("a second client behind the same proxy returned %d; it must not inherit the first one's count", status)
	}
	if status := pull("198.51.100.7"); status != http.StatusTooManyRequests {
		t.Fatalf("the first client's second pull returned %d, want 429", status)
	}
}

func TestSubscriptionAccessLogDropsRowsPastRetention(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	stale := time.Now().UTC().Add(-subscriptionAccessRetention - 24*time.Hour).Unix()
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO subscription_access_logs
		(id, config_id, config_name, ip, location, user_agent, accessed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"stale-row", "config", "phone", "198.51.100.7", "", "clash", stale); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSubscriptionAccess(t.Context(), "config", "phone", "203.0.113.9", "", "clash"); err != nil {
		t.Fatal(err)
	}

	var remaining int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM subscription_access_logs WHERE id = 'stale-row'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("a row past the retention window survived the prune")
	}
	var kept int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM subscription_access_logs`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("prune left %d rows, want only the one just recorded", kept)
	}
}

// DropSubscriptionAccessTable lets the external test package observe what a
// subscription pull does when the audit write fails. It lives here because
// only the internal test package can reach the database handle.
func DropSubscriptionAccessTable(ctx context.Context, store *Store) error {
	_, err := store.db.ExecContext(ctx, `DROP TABLE subscription_access_logs`)
	return err
}

// SetSubscriptionClock lets the external test package reach a specific moment
// when exercising the access window and the expiry.
func SetSubscriptionClock(server *Server, now func() time.Time) {
	server.now = now
}

func TestAccessWindowBounds(t *testing.T) {
	at := func(hour, minute int) time.Time {
		return time.Date(2026, 9, 2, hour, minute, 0, 0, time.Local)
	}
	daytimeStart, daytimeEnd := 8*60, 23*60
	nightStart, nightEnd := 22*60, 6*60

	for _, testCase := range []struct {
		name       string
		start, end int
		at         time.Time
		wantWithin bool
	}{
		{"an unset window admits any hour", unsetAccessWindowBound, unsetAccessWindowBound, at(3, 0), true},
		{"inside a daytime window", daytimeStart, daytimeEnd, at(12, 0), true},
		{"on the opening edge", daytimeStart, daytimeEnd, at(8, 0), true},
		{"on the closing edge", daytimeStart, daytimeEnd, at(23, 0), true},
		{"before a daytime window opens", daytimeStart, daytimeEnd, at(7, 59), false},
		{"after a daytime window closes", daytimeStart, daytimeEnd, at(23, 1), false},
		// A window whose start is past its end wraps around midnight; reading
		// it literally would make it match nothing at all.
		{"late evening of an overnight window", nightStart, nightEnd, at(23, 30), true},
		{"early morning of an overnight window", nightStart, nightEnd, at(5, 0), true},
		{"midday is outside an overnight window", nightStart, nightEnd, at(12, 0), false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := withinAccessWindow(testCase.at, testCase.start, testCase.end); got != testCase.wantWithin {
				t.Fatalf("withinAccessWindow = %v, want %v", got, testCase.wantWithin)
			}
		})
	}
}

func TestAccessSecretRejectsUnusableText(t *testing.T) {
	// Anything outside the alphabet could not be pulled back out of a
	// User-Agent, so it has to be refused at the point it is set.
	for _, secret := range []string{"短", "with space", "has/slash", "short"} {
		if _, err := normalizeAccessSecret(secret); err == nil {
			t.Fatalf("secret %q should have been rejected", secret)
		}
	}
	kept, err := normalizeAccessSecret("  Family-Phone_2026  ")
	if err != nil || kept != "Family-Phone_2026" {
		t.Fatalf("normalizeAccessSecret trimmed to %q, err %v", kept, err)
	}
}
