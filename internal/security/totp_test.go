package security

import (
	"testing"
	"time"
)

func TestVerifyTOTPAroundFindsTheMatchingStep(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, steps := range []int64{-16, -3, -1, 0, 1, 3, 16} {
		code := totpCode(secret, now.Add(time.Duration(steps)*totpPeriod))
		matched, ok := VerifyTOTPAround(secret, code, now, 0, 16)
		if !ok || matched != steps {
			t.Fatalf("step %d: matched=%d ok=%v", steps, matched, ok)
		}
	}
	outside := totpCode(secret, now.Add(2*totpPeriod))
	if _, ok := VerifyTOTPAround(secret, outside, now, 0, 1); ok {
		t.Fatal("accepted a code outside the search radius")
	}
	recentered, ok := VerifyTOTPAround(secret, outside, now, 3, 1)
	if !ok || recentered != 2 {
		t.Fatalf("searching around a stored offset: matched=%d ok=%v", recentered, ok)
	}
	if _, ok := VerifyTOTPAround(secret, " 12345 ", now, 0, 1); ok {
		t.Fatal("accepted a code that is not six digits")
	}
}
