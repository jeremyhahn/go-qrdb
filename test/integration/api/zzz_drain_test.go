// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestZZZAdminDrain tests the admin drain command across all protocols.
// Drain is destructive -- the node enters drain mode and stops accepting
// new writes.
//
// The function name starts with "ZZZ" so it sorts last alphabetically
// among all test functions in this package. Go runs tests in alphabetical
// order by function name, so drain executes after every other test,
// preventing write-rejection from affecting preceding tests.
func TestZZZAdminDrain(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "drain")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin drain failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin drain produced no output", proto))
			assertContains(t, output, "OK",
				fmt.Sprintf("[%s] admin drain should contain 'OK'", proto))

			t.Logf("[%s] admin drain: %s", proto, strings.TrimSpace(output))
		})
	}
}
