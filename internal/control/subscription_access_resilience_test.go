package control_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

// The access trail is an audit aid, not a precondition. A client entitled to
// its configuration must still receive it when the log write fails; otherwise
// a broken audit table takes every client offline with it.
func TestSubscriptionSurvivesAccessLogFailure(t *testing.T) {
	store, groupID := newMihomoDNSFixture(t)
	config, err := store.CreateMihomoClientConfig(t.Context(), mihomoDNSConfig(groupID))
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.DropSubscriptionAccessTable(t.Context(), store); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, config.SubscriptionPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("pull returned %d after the access log write failed, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "proxies:") {
		t.Fatalf("pull returned a body without proxies:\n%s", recorder.Body.String())
	}
}
