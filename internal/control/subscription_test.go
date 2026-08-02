package control

import (
	"strings"
	"testing"
)

func TestValidateSubscriptionOnlyAcceptsClientSubscriptions(t *testing.T) {
	err := ValidateSubscription(Subscription{
		Kind:        SubscriptionKind("upstream"),
		Name:        "remote rules",
		URL:         "https://example.com/rules.json",
		EndpointIDs: []string{"endpoint"},
	})
	if err == nil || !strings.Contains(err.Error(), "only client subscriptions") {
		t.Fatalf("expected remote rule subscription to be rejected, got %v", err)
	}

	err = ValidateSubscription(Subscription{
		Kind:        ClientSubscription,
		Name:        "phone",
		EndpointIDs: []string{"endpoint"},
	})
	if err != nil {
		t.Fatalf("expected client subscription to be accepted, got %v", err)
	}
}
