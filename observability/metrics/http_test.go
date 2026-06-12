package metrics

import (
	"testing"
)

// TestStatusClass verifies the HTTP status bucketing helper.
// statusClass is now exported as StatusClass; this test is kept in the core
// package to avoid losing coverage of the pure helper.
func TestStatusClass(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{100, "1xx"},
		{204, "2xx"},
		{302, "3xx"},
		{404, "4xx"},
		{503, "5xx"},
		{700, "unknown"},
	} {
		if got := StatusClass(tc.status); got != tc.want {
			t.Fatalf("StatusClass(%d) = %s, want %s", tc.status, got, tc.want)
		}
	}
}
