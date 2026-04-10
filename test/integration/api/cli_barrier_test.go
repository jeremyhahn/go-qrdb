// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
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
// lifecycle: init → status (unsealed) → seal → status (sealed) → unseal
// → status (unsealed). Uses a single protocol to avoid concurrent
// barrier state mutations across protocol tests.
func TestCLIMultiProtocolBarrierInitAndSealCycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	// Use a single protocol for lifecycle testing to avoid
	// concurrent state mutations. Prefer unix as it's most reliable.
	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			secret := "integration-test-secret"

			// Step 1: Initialize the barrier with a secret.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "init", "--secret", secret)
			if err != nil {
				output := stdout + stderr
				// If already initialized, that's OK — continue with the test.
				if !strings.Contains(output, "already initialized") &&
					!strings.Contains(output, "already_initialized") {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] barrier init failed: %v", proto, err)
				}
				t.Logf("[%s] barrier already initialized, continuing with lifecycle test", proto)
			} else {
				output := stdout + stderr
				assertContains(t, output, "OK",
					fmt.Sprintf("[%s] barrier init should return OK", proto))
			}

			// Step 2: Verify barrier is unsealed after init.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status after init failed: %v", proto, err)
			}
			output := stdout + stderr
			assertContains(t, output, `"sealed": false`,
				fmt.Sprintf("[%s] barrier should be unsealed after init", proto))

			// Step 3: Seal the barrier.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"barrier", "seal")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier seal failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, "OK",
				fmt.Sprintf("[%s] barrier seal should return OK", proto))

			// Step 4: Verify barrier is sealed.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status after seal failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, `"sealed": true`,
				fmt.Sprintf("[%s] barrier should be sealed after seal", proto))

			// Step 5: Unseal the barrier.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"barrier", "unseal", "--secret", secret)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier unseal failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, "OK",
				fmt.Sprintf("[%s] barrier unseal should return OK", proto))

			// Step 6: Verify barrier is unsealed again.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"barrier", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier status after unseal failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, `"sealed": false`,
				fmt.Sprintf("[%s] barrier should be unsealed after unseal", proto))

			t.Logf("[%s] barrier init/seal/unseal cycle passed", proto)
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

// TestCLIMultiProtocolBarrierShamirListShares tests the shamir list-shares
// command to verify share metadata is returned.
func TestCLIMultiProtocolBarrierShamirListShares(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "shamir", "list-shares", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				// Shamir may not be configured — skip gracefully.
				if strings.Contains(stdout+stderr, "not configured") ||
					strings.Contains(stdout+stderr, "shamir not") {
					t.Skipf("[%s] shamir not configured, skipping", proto)
				}
				t.Fatalf("[%s] barrier shamir list-shares failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "count",
				fmt.Sprintf("[%s] shamir list-shares should contain 'count' field", proto))

			t.Logf("[%s] barrier shamir list-shares passed", proto)
		})
	}
}

// TestCLIMultiProtocolBarrierShamirVerify tests the shamir verify command.
func TestCLIMultiProtocolBarrierShamirVerify(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range barrierSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"barrier", "shamir", "verify")
			if err != nil {
				output := stdout + stderr
				// Shamir may not be configured — skip gracefully.
				if strings.Contains(output, "not configured") ||
					strings.Contains(output, "shamir not") {
					t.Skipf("[%s] shamir not configured, skipping", proto)
				}
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] barrier shamir verify failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "OK",
				fmt.Sprintf("[%s] shamir verify should return OK", proto))

			t.Logf("[%s] barrier shamir verify passed", proto)
		})
	}
}
