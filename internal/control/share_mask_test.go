package control

import "testing"

func TestMaskShareAddressKeepsShapeButHidesHost(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"203.0.113.25":       "203.0.*.*",
		"203.0.113.25:443":   "203.0.*.*:443",
		"2001:db8::1":        "2001:0db8:****",
		"[2001:db8::1]:8443": "[2001:0db8:****]:8443",
		"sg.example.com":     "***.com",
		"sg.example.com:443": "***.com:443",
		"internal":           "***",
	}
	for input, want := range cases {
		if got := maskShareAddress(input); got != want {
			t.Fatalf("maskShareAddress(%q) = %q, want %q", input, got, want)
		}
	}
}
