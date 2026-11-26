package server

import "testing"

// TestWithVersion_Status asserts WithVersion records the build version the
// REST GET /status reports.
func TestWithVersion_Status(t *testing.T) {
	var o serverOptions
	WithVersion("1.2.3-test")(&o)
	if o.version != "1.2.3-test" {
		t.Fatalf("serverOptions.version = %q, want %q", o.version, "1.2.3-test")
	}
}
