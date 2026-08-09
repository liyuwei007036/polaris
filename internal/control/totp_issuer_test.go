package control

import "testing"

func TestTOTPIssuerStripsPortAndRejectsColons(t *testing.T) {
	for _, testCase := range []struct {
		host string
		want string
	}{
		{"panel.example.com", "panel.example.com"},
		{"panel.example.com:8443", "panel.example.com"},
		{"127.0.0.1:9000", "127.0.0.1"},
		{"[::1]:9000", "Polaris"},
		{"::1", "Polaris"},
		{"", "Polaris"},
	} {
		if got := totpIssuer(testCase.host); got != testCase.want {
			t.Errorf("totpIssuer(%q) = %q, want %q", testCase.host, got, testCase.want)
		}
	}
}
