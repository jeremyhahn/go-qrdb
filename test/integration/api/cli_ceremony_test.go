// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// ceremonySupportedProtocols returns protocols that support the ceremony API.
func ceremonySupportedProtocols() []ProtocolType {
	return AllProtocols()
}

// TestCLIMultiProtocolCeremonyStatus tests the ceremony status command
// across all protocols.
func TestCLIMultiProtocolCeremonyStatus(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range ceremonySupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ceremony", "status", "--output", "json")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ceremony status failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "state",
				fmt.Sprintf("[%s] ceremony status should contain 'state' field", proto))

			t.Logf("[%s] ceremony status passed", proto)
		})
	}
}

// TestCLIMultiProtocolCeremonyStatusTableFormat tests the ceremony status
// command with the default table output format.
func TestCLIMultiProtocolCeremonyStatusTableFormat(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range ceremonySupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ceremony", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ceremony status table format failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "State:",
				fmt.Sprintf("[%s] table output should contain 'State:' label", proto))

			t.Logf("[%s] ceremony status table format passed", proto)
		})
	}
}

// TestCLIMultiProtocolCeremonyStatusYAMLFormat tests the ceremony status
// command with YAML output format.
func TestCLIMultiProtocolCeremonyStatusYAMLFormat(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range ceremonySupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ceremony", "status", "--output", "yaml")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ceremony status YAML format failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "state:",
				fmt.Sprintf("[%s] YAML output should contain 'state:' field", proto))

			t.Logf("[%s] ceremony status YAML format passed", proto)
		})
	}
}
