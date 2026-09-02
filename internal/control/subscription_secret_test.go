package control_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/control"
)

func newAccessFixture(t *testing.T) (*control.Store, *control.Server, string, control.MihomoClientConfig) {
	t.Helper()
	store, groupID := newMihomoDNSFixture(t)
	config, err := store.CreateMihomoClientConfig(t.Context(), mihomoDNSConfig(groupID))
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	return store, server, groupID, config
}

// setAccess rewrites only the access fences, leaving the rest of the
// configuration exactly as the fixture built it.
func setAccess(t *testing.T, store *control.Store, groupID, configID string, mutate func(*control.MihomoClientConfig)) control.MihomoClientConfig {
	t.Helper()
	input := mihomoDNSConfig(groupID)
	input.ID = configID
	mutate(&input)
	updated, err := store.UpdateMihomoClientConfig(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func pullSubscription(t *testing.T, server *control.Server, path, userAgent string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("User-Agent", userAgent)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// A configuration with no fences set has to behave exactly as it did before
// they existed, or every subscription already in the field breaks on upgrade.
func TestSubscriptionWithoutFencesIsUnchanged(t *testing.T) {
	_, server, _, config := newAccessFixture(t)
	if config.AccessSecret != "" || config.AccessWindowStart != "" || config.AccessExpiresAt != "" {
		t.Fatalf("a new configuration must start unfenced, got %#v", config)
	}
	if code := pullSubscription(t, server, config.SubscriptionPath, "clash-verge/2.0").Code; code != http.StatusOK {
		t.Fatalf("pull returned %d, want 200", code)
	}
}

func TestAccessSecretIsOperatorChosenAndOptional(t *testing.T) {
	store, server, groupID, config := newAccessFixture(t)

	updated := setAccess(t, store, groupID, config.ID, func(input *control.MihomoClientConfig) {
		input.AccessSecret = "  Family-Phone_2026  "
	})
	if updated.AccessSecret != "Family-Phone_2026" {
		t.Fatalf("stored secret is %q, want the trimmed operator value", updated.AccessSecret)
	}
	if updated.AccessUserAgent != "polaris/Family-Phone_2026" {
		t.Fatalf("rendered header is %q", updated.AccessUserAgent)
	}

	for _, testCase := range []struct {
		name  string
		agent string
		want  int
	}{
		{"no user agent at all", "", http.StatusNotFound},
		{"a browser opening the address directly", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", http.StatusNotFound},
		{"a client carrying the wrong secret", "polaris/some-other-secret", http.StatusNotFound},
		{"the secret on its own", "polaris/Family-Phone_2026", http.StatusOK},
		// Matched anywhere in the header, so a client may keep its own product
		// string beside it.
		{"the secret beside a product string", "clash-verge/2.0 polaris/Family-Phone_2026 (windows)", http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if code := pullSubscription(t, server, config.SubscriptionPath, testCase.agent).Code; code != testCase.want {
				t.Fatalf("pull returned %d, want %d", code, testCase.want)
			}
		})
	}

	// Clearing the field turns the check off again rather than locking the
	// subscription behind a secret nobody can withdraw.
	cleared := setAccess(t, store, groupID, config.ID, func(input *control.MihomoClientConfig) { input.AccessSecret = "" })
	if cleared.AccessSecret != "" || cleared.AccessUserAgent != "" {
		t.Fatalf("secret survived being cleared: %#v", cleared)
	}
	if code := pullSubscription(t, server, config.SubscriptionPath, "clash-verge/2.0").Code; code != http.StatusOK {
		t.Fatalf("pull after clearing the secret returned %d, want 200", code)
	}

	// A secret that could never be recovered from a User-Agent is refused
	// where it is set, not stored and then silently never matched.
	unusable := mihomoDNSConfig(groupID)
	unusable.ID = config.ID
	unusable.AccessSecret = "带 空格"
	if _, err := store.UpdateMihomoClientConfig(t.Context(), unusable); err == nil {
		t.Fatal("a secret outside the usable alphabet should have been rejected")
	}
}

func TestAccessWindowClosesTheSubscription(t *testing.T) {
	store, server, groupID, config := newAccessFixture(t)
	updated := setAccess(t, store, groupID, config.ID, func(input *control.MihomoClientConfig) {
		input.AccessWindowStart, input.AccessWindowEnd = "08:00", "23:00"
	})
	if updated.AccessWindowStart != "08:00" || updated.AccessWindowEnd != "23:00" {
		t.Fatalf("window did not round trip: %#v", updated)
	}

	at := func(hour, minute int) time.Time { return time.Date(2026, 9, 2, hour, minute, 0, 0, time.Local) }
	for _, testCase := range []struct {
		name string
		now  time.Time
		want int
	}{
		{"inside the window", at(12, 0), http.StatusOK},
		{"before it opens", at(7, 0), http.StatusNotFound},
		{"after it closes", at(23, 30), http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			control.SetSubscriptionClock(server, func() time.Time { return testCase.now })
			if code := pullSubscription(t, server, config.SubscriptionPath, "clash-verge/2.0").Code; code != testCase.want {
				t.Fatalf("pull returned %d, want %d", code, testCase.want)
			}
		})
	}

	// One bound on its own is ambiguous, so a half-filled window is refused.
	halfWindow := mihomoDNSConfig(groupID)
	halfWindow.ID = config.ID
	halfWindow.AccessWindowStart = "08:00"
	if _, err := store.UpdateMihomoClientConfig(t.Context(), halfWindow); err == nil {
		t.Fatal("a window missing its closing bound should have been rejected")
	}
}

func TestAccessExpiryRetiresTheSubscription(t *testing.T) {
	store, server, groupID, config := newAccessFixture(t)
	expiry := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	updated := setAccess(t, store, groupID, config.ID, func(input *control.MihomoClientConfig) {
		input.AccessExpiresAt = expiry.Format(time.RFC3339)
	})
	if updated.AccessExpiresAt != expiry.Format(time.RFC3339) {
		t.Fatalf("expiry did not round trip: %q", updated.AccessExpiresAt)
	}

	control.SetSubscriptionClock(server, func() time.Time { return expiry.Add(-time.Hour) })
	if code := pullSubscription(t, server, config.SubscriptionPath, "clash-verge/2.0").Code; code != http.StatusOK {
		t.Fatalf("pull before the expiry returned %d, want 200", code)
	}
	control.SetSubscriptionClock(server, func() time.Time { return expiry.Add(time.Second) })
	if code := pullSubscription(t, server, config.SubscriptionPath, "clash-verge/2.0").Code; code != http.StatusNotFound {
		t.Fatalf("pull after the expiry returned %d, want 404", code)
	}
}

// The access trail is stored in the clear and shown in the console, so the
// secret must never reach it.
func TestAccessLogRedactsTheSecret(t *testing.T) {
	store, server, groupID, config := newAccessFixture(t)
	updated := setAccess(t, store, groupID, config.ID, func(input *control.MihomoClientConfig) {
		input.AccessSecret = "Family-Phone_2026"
	})
	if code := pullSubscription(t, server, config.SubscriptionPath, updated.AccessUserAgent).Code; code != http.StatusOK {
		t.Fatalf("pull returned %d, want 200", code)
	}

	items, _, err := store.ListSubscriptionAccess(t.Context(), control.SubscriptionAccessFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one access row, got %d", len(items))
	}
	if strings.Contains(items[0].UserAgent, updated.AccessSecret) {
		t.Fatalf("access log stored the secret in the clear: %q", items[0].UserAgent)
	}
	if !strings.Contains(items[0].UserAgent, "polaris/***") {
		t.Fatalf("access log should keep a redacted marker, got %q", items[0].UserAgent)
	}
}
