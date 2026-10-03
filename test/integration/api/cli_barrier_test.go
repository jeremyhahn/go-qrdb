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
// status (unsealed) → single-caller seal refused → seal quorum → status
// (sealed) → unseal quorum → status (unsealed). The barrier is initialized by
// the qrdbd-shamir bootstrap, which enrols three share holders and claims
// their envelopes; each quorum submission here presents the holder's own
// certificate, answers the mtls factor with its key and opens its envelope
// locally. Uses a single protocol per subtest to avoid concurrent barrier
// state mutations.
func TestCLIMultiProtocolBarrierInitAndSealCycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	holders := loadShamirHolders(t, cfg)
	if len(holders) < 2 {
		t.Fatalf("need at least threshold (2) holders from the bootstrap holders file, got %d", len(holders))
	}

	shamir := cfg.shamirConfig()

	// submit runs one holder's quorum submission for op over proto.
	submit := func(t *testing.T, proto ProtocolType, op string, h shamirHolder) string {
		t.Helper()
		stdout, stderr, err := execCLIWithProtocolAndCert(t, shamir, proto, h.Cert, h.Key,
			"barrier", "quorum", "submit", "--op", op, "--username", h.Username,
			"--envelope", h.Envelope, "--key", h.Key, "--output", "json")
		if err != nil {
			t.Logf("stdout: %s", stdout)
			t.Logf("stderr: %s", stderr)
			t.Fatalf("[%s] quorum submit --op %s %s failed: %v", proto, op, h.Username, err)
		}
		return stdout + stderr
	}
	// sealed reports the barrier's sealed state over proto.
	sealed := func(t *testing.T, proto ProtocolType) string {
		t.Helper()
		stdout, stderr, err := execCLIWithShamirProtocol(t, cfg, proto, "barrier", "status", "--output", "json")
		if err != nil {
			t.Logf("stdout: %s", stdout)
			t.Logf("stderr: %s", stderr)
			t.Fatalf("[%s] barrier status failed: %v", proto, err)
		}
		return stdout + stderr
	}

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !shamir.IsProtocolAvailable(t, proto) {
				t.Fatalf("shamir server not available for protocol %s", proto)
			}

			// Step 1: the bootstrap left the barrier unsealed.
			assertContains(t, sealed(t, proto), `"sealed": false`,
				fmt.Sprintf("[%s] shamir barrier should be unsealed at start", proto))

			// Step 2a: a single-caller seal is refused on a Shamir barrier
			// (ErrShamirBarrierRequiresQuorum).
			stdout, stderr, err := execCLIWithShamirProtocol(t, cfg, proto,
				"barrier", "seal", "--assume-yes")
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier seal should be rejected on a shamir barrier (quorum required)", proto)
			}

			// Step 2b: two holders seal it; the second completes the round.
			submit(t, proto, "seal", holders[0])
			assertContains(t, submit(t, proto, "seal", holders[1]), `"complete": true`,
				fmt.Sprintf("[%s] the second seal submission should complete the quorum", proto))

			// Step 3: sealed.
			assertContains(t, sealed(t, proto), `"sealed": true`,
				fmt.Sprintf("[%s] barrier should be sealed after the seal quorum", proto))

			// Step 4: two holders unseal it.
			submit(t, proto, "unseal", holders[0])
			assertContains(t, submit(t, proto, "unseal", holders[1]), `"complete": true`,
				fmt.Sprintf("[%s] the second unseal submission should complete the quorum", proto))

			// Step 5: unsealed again.
			assertContains(t, sealed(t, proto), `"sealed": false`,
				fmt.Sprintf("[%s] barrier should be unsealed after the unseal quorum", proto))

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

// TestCLIMultiProtocolBarrierClusterUnseal tests `barrier cluster-unseal` and
// the cluster_seal object of `barrier status` across all protocols. The
// compose server is a single node, so cluster sealing is not active there:
// status reports it inactive, and cluster-unseal is refused with the typed
// not-active error on every protocol rather than pretending to clear a record.
func TestCLIMultiProtocolBarrierClusterUnseal(t *testing.T) {
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
				t.Fatalf("[%s] barrier status failed: %v\nstdout: %s\nstderr: %s", proto, err, stdout, stderr)
			}
			assertContains(t, stdout+stderr, `"cluster_seal"`,
				fmt.Sprintf("[%s] barrier status should carry cluster_seal", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"barrier", "cluster-unseal", "--output", "json")
			if err == nil {
				t.Fatalf("[%s] cluster-unseal succeeded on a node without cluster sealing\nstdout: %s", proto, stdout)
			}
			assertContains(t, stdout+stderr, "not active",
				fmt.Sprintf("[%s] cluster-unseal should report cluster sealing not active", proto))

			t.Logf("[%s] barrier cluster-unseal passed", proto)
		})
	}
}
