// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// barrierSupportedProtocols returns protocols that support the barrier API.
// All protocols support barrier operations (no streaming required).
func barrierSupportedProtocols() []ProtocolType {
	return AllProtocols()
}

// TestCLIMultiProtocolBarrierStatus tests the barrier status command
// across all protocols. The barrier starts sealed and uninitialized
// when the server first boots with seal enabled.
func TestCLIMultiProtocolBarrierStatus(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "sealed",
				fmt.Sprintf("[%s] barrier status should contain 'sealed' field", proto))
			assertContains(t, output, "strategy",
				fmt.Sprintf("[%s] barrier status should contain 'strategy' field", proto))

			t.Logf("[%s] barrier status passed", proto)
		})
	}
}

// TestCLIMultiProtocolBarrierInitAndSealCycle tests the full barrier
// seal/unseal lifecycle against the dedicated Shamir-seal server:
// status (unsealed) → seal-vote quorum → status (sealed) → unseal-shares
// → status (unsealed). The barrier is initialized by the qrdbd-shamir
// bootstrap, which registers three Security Officers and claims their
// shares into the shares file, so this test exercises the quorum-gated
// seal half of the cycle (single-admin `barrier seal` is rejected for
// Shamir barriers) and drives unseal via Shamir shares (threshold
// M-of-N), not a software passphrase. Uses a single protocol per subtest
// to avoid concurrent barrier state mutations.
func TestCLIMultiProtocolBarrierInitAndSealCycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	// Load the Shamir shares claimed by the qrdbd-shamir bootstrap's
	// Security Officers so we can replay enough of them (>= threshold)
	// for both the seal-vote quorum and unseal.
	shares := loadShamirShares(t, cfg)
	if len(shares) < 2 {
		t.Fatalf("need at least threshold (2) shares from the bootstrap shares file, got %d", len(shares))
	}

	shamir := cfg.shamirConfig()

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !shamir.IsProtocolAvailable(t, proto) {
				t.Fatalf("shamir server not available for protocol %s", proto)
			}

			// Step 1: Verify barrier is unsealed (bootstrap unsealed it).
			stdout, stderr, err := execCLIWithShamirProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status failed: %v", proto, err)
			}
			output := stdout + stderr
			assertContains(t, output, `"sealed": false`,
				fmt.Sprintf("[%s] shamir barrier should be unsealed at start", proto))

			// Step 2a: Single-admin seal must be rejected on a Shamir
			// barrier (ErrShamirBarrierRequiresQuorum) — sealing requires
			// M distinct Security Officer share votes.
			stdout, stderr, err = execCLIWithShamirProtocol(t, cfg, proto,
				"barrier", "seal", "--assume-yes")
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier seal should be rejected on a shamir barrier (quorum required)", proto)
			}

			// Step 2b: Seal the barrier via the SO seal-vote quorum. The
			// dedicated server is configured with threshold 2; submit two
			// distinct shares so the quorum completes on the second vote.
			for i, share := range shares[:2] {
				stdout, stderr, err = execCLIWithShamirProtocol(t, cfg, proto,
					"barrier", "shamir-seal-vote", "--share", share, "--output", "json")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] barrier shamir-seal-vote %d failed: %v", proto, i+1, err)
				}
			}
			output = stdout + stderr
			assertContains(t, output, `"complete": true`,
				fmt.Sprintf("[%s] final seal vote should report quorum complete", proto))

			// Step 3: Verify barrier is sealed.
			stdout, stderr, err = execCLIWithShamirProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status after seal failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, `"sealed": true`,
				fmt.Sprintf("[%s] barrier should be sealed after seal", proto))

			// Step 4: Unseal the barrier with Shamir shares (M-of-N). The
			// dedicated server is configured with threshold 2; submit all
			// captured shares so the quorum completes in a single call.
			unsealArgs := []string{"barrier", "unseal-shares"}
			for _, s := range shares {
				unsealArgs = append(unsealArgs, "--share", s)
			}
			stdout, stderr, err = execCLIWithShamirProtocol(t, cfg, proto, unsealArgs...)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier unseal-shares failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, "OK",
				fmt.Sprintf("[%s] barrier unseal-shares should return OK", proto))

			// Step 5: Verify barrier is unsealed again.
			stdout, stderr, err = execCLIWithShamirProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status after unseal failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, `"sealed": false`,
				fmt.Sprintf("[%s] barrier should be unsealed after unseal-shares", proto))

			t.Logf("[%s] shamir barrier seal/unseal cycle passed", proto)
		})
	}
}

// TestCLIMultiProtocolBarrierStatusTableFormat tests the barrier status
// command with the default table output format.
func TestCLIMultiProtocolBarrierStatusTableFormat(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status table format failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "Sealed:",
				fmt.Sprintf("[%s] table output should contain 'Sealed:' label", proto))
			assertContains(t, output, "Strategy:",
				fmt.Sprintf("[%s] table output should contain 'Strategy:' label", proto))

			t.Logf("[%s] barrier status table format passed", proto)
		})
	}
}

// TestCLIMultiProtocolBarrierStatusYAMLFormat tests the barrier status
// command with YAML output format.
func TestCLIMultiProtocolBarrierStatusYAMLFormat(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "status", "--output", "yaml")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status YAML format failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "sealed:",
				fmt.Sprintf("[%s] YAML output should contain 'sealed:' field", proto))
			assertContains(t, output, "strategy:",
				fmt.Sprintf("[%s] YAML output should contain 'strategy:' field", proto))

			t.Logf("[%s] barrier status YAML format passed", proto)
		})
	}
}

// TestCLIMultiProtocolBarrierRecoveryStatus tests the recovery status
// command to check if recovery keys exist.
func TestCLIMultiProtocolBarrierRecoveryStatus(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "recovery", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier recovery status failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "has_keys",
				fmt.Sprintf("[%s] recovery status should contain 'has_keys' field", proto))

			t.Logf("[%s] barrier recovery status passed", proto)
		})
	}
}
