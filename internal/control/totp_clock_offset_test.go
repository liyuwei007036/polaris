package control_test

import (
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/control"
)

// TestTOTPLoginSurvivesAuthenticatorClockSkew reproduces the "login only
// succeeds after many attempts" failure: the authenticator's clock is minutes
// away from the server's (a VPS without NTP, WSL after host sleep), so codes
// fall outside the fixed ±1-step window. Enrollment must learn the skew and
// login must verify around it, re-anchoring as the clocks keep drifting.
func TestTOTPLoginSurvivesAuthenticatorClockSkew(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admin, _, err := store.EnsureDefaultAdmin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	password := "Clock-Skew-Password-2026!"
	if err := store.ChangeOwnPassword(t.Context(), admin.ID, control.DefaultAdminPassword, password, ""); err != nil {
		t.Fatal(err)
	}

	// The authenticator runs 3 steps (90s) ahead of the server.
	skew := 3 * 30 * time.Second
	secret, err := store.BeginOperatorTOTPSetup(t.Context(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmOperatorTOTP(t.Context(), admin.ID, totp(secret, time.Now().UTC().Add(skew))); err != nil {
		t.Fatalf("enroll with a skewed authenticator clock: %v", err)
	}

	loginWithSkew := func(skew time.Duration) error {
		result, err := store.StartLogin(t.Context(), control.DefaultAdminUsername, password)
		if err != nil {
			t.Fatal(err)
		}
		if !result.RequiresTOTP {
			t.Fatalf("expected a TOTP challenge: %#v", result)
		}
		_, err = store.FinishLogin(t.Context(), result.ChallengeID, totp(secret, time.Now().UTC().Add(skew)))
		return err
	}

	// Every login with the same skew must pass on the first attempt.
	for attempt := 0; attempt < 3; attempt++ {
		if err := loginWithSkew(skew); err != nil {
			t.Fatalf("login attempt %d with enrolled skew: %v", attempt, err)
		}
	}

	// The clocks keep drifting apart; each successful login re-anchors, so a
	// cumulative drift far past the enrolled skew still logs in first try.
	for _, steps := range []int{4, 5, 6} {
		if err := loginWithSkew(time.Duration(steps) * 30 * time.Second); err != nil {
			t.Fatalf("login after drifting to %d steps: %v", steps, err)
		}
	}

	// A code far outside the anchored window must still be rejected.
	if err := loginWithSkew(9 * 30 * time.Second); err == nil {
		t.Fatal("accepted a code 3 steps past the anchored offset")
	}
}
