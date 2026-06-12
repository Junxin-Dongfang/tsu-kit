package metrics

import (
	"testing"
)

// TestPortFromAddr verifies port extraction for various address formats.
func TestPortFromAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want string
	}{
		{"", "9091"},
		{":9091", "9091"},
		{"127.0.0.1:19091", "19091"},
		{"9092", "9092"},
	} {
		if got := PortFromAddr(tc.addr); got != tc.want {
			t.Fatalf("PortFromAddr(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
