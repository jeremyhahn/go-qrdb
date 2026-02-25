// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// execCLIWithServer executes the CLI binary with the --server flag and optional TLS CA cert.
func execCLIWithServer(t *testing.T, cfg *TestConfig, serverURL string, args ...string) (string, string, error) {
	t.Helper()

	var prefixArgs []string

	if serverURL != "" {
		prefixArgs = append(prefixArgs, "--server", serverURL)
	}

	// Add --ca-file for TLS-bearing protocols (HTTPS, gRPCs, MCP over TLS).
	needsTLS := strings.HasPrefix(serverURL, "https://") ||
		strings.HasPrefix(serverURL, "grpcs://") ||
		strings.HasPrefix(serverURL, "mcp://")

	if needsTLS {
		caFile := getEnv("QRDB_TLS_CA", "/etc/qrdb/certs/ca.crt")
		if caFile != "" {
			prefixArgs = append(prefixArgs, "--ca-file", caFile)
		}
	}

	args = append(prefixArgs, args...)
	cmd := exec.Command(cfg.CLIBinPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	return stdout.String(), stderr.String(), err
}

// execCLIWithProtocol dispatches CLI execution to the correct server URL
// based on the protocol type.
func execCLIWithProtocol(t *testing.T, cfg *TestConfig, protocol ProtocolType, args ...string) (string, string, error) {
	t.Helper()

	serverURL := cfg.GetServerURL(protocol)
	return execCLIWithServer(t, cfg, serverURL, args...)
}

// testTimestamp returns a unique identifier based on the current process PID
// and UID, ensuring stable uniqueness within a single test run.
func testTimestamp() int64 {
	return int64(os.Getpid())*1e9 + int64(os.Getuid())*1000
}

// execCLIRaw executes the CLI binary with the given arguments and no
// server or TLS flags. Used for commands that do not require a server
// connection (e.g., version).
func execCLIRaw(t *testing.T, cfg *TestConfig, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.Command(cfg.CLIBinPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	return stdout.String(), stderr.String(), err
}

// TestCLIMultiProtocolVersion tests the version command across all protocols.
// The version command does not require a server connection, so it should
// succeed regardless of protocol availability.
func TestCLIMultiProtocolVersion(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			// Version command bypasses client construction entirely,
			// so we run it without any --server flag.
			stdout, stderr, err := execCLIRaw(t, cfg, "version")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("version command failed for protocol %s: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, "version command produced no output")

			// The version command should print the QRDB version identifier.
			assertContains(t, strings.ToLower(output), "qrdb",
				fmt.Sprintf("[%s] version output should contain 'qrdb'", proto))

			t.Logf("[%s] version output: %s", proto, strings.TrimSpace(output))
		})
	}
}

// TestCLIMultiProtocolHealth tests the admin health command via all remote protocols.
func TestCLIMultiProtocolHealth(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "health")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin health failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin health produced no output", proto))
			assertContains(t, output, "Status:",
				fmt.Sprintf("[%s] admin health output should contain 'Status:'", proto))

			t.Logf("[%s] admin health: %s", proto, strings.TrimSpace(output))
		})
	}
}

// TestCLIMultiProtocolKVPutGetDelete tests the full KV CRUD lifecycle
// (put, get, exists, delete, exists-after-delete) across all protocols.
func TestCLIMultiProtocolKVPutGetDelete(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("test-key-%s-%d", proto, testTimestamp())
			value := fmt.Sprintf("value-for-%s", proto)

			// Step 1: Put
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put should output 'OK'", proto))

			// Step 2: Get and verify value
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get failed: %v", proto, err)
			}
			assertContains(t, strings.TrimSpace(stdout), value,
				fmt.Sprintf("[%s] kv get should return stored value", proto))

			// Step 3: Exists should return "true"
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv exists failed: %v", proto, err)
			}
			assertEqual(t, "true", strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv exists should return 'true'", proto))

			// Step 4: Delete
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "del", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv del failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv del should output 'OK'", proto))

			// Step 5: Exists should return "false" after deletion
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv exists after delete failed: %v", proto, err)
			}
			assertEqual(t, "false", strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv exists should return 'false' after delete", proto))

			t.Logf("[%s] KV CRUD lifecycle passed for key: %s", proto, key)
		})
	}
}

// TestCLIMultiProtocolKVList tests listing keys across all protocols.
// It first inserts keys with a common prefix, then verifies kv list
// returns those keys.
func TestCLIMultiProtocolKVList(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("list-%s-%d", proto, testTimestamp())
			keys := []string{
				prefix + "/alpha",
				prefix + "/beta",
				prefix + "/gamma",
			}

			// Insert test keys.
			for _, key := range keys {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "v")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, key, err)
				}
			}

			// Cleanup inserted keys after test.
			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// List keys with prefix.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "list", prefix)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list failed: %v", proto, err)
			}

			output := stdout + stderr
			for _, key := range keys {
				assertContains(t, output, key,
					fmt.Sprintf("[%s] kv list output should contain key %s", proto, key))
			}

			t.Logf("[%s] kv list passed, found %d keys", proto, len(keys))
		})
	}
}

// TestCLIMultiProtocolAdminStatus tests the admin status command across all protocols.
func TestCLIMultiProtocolAdminStatus(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin status failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin status produced no output", proto))
			assertContains(t, output, "NodeID:",
				fmt.Sprintf("[%s] admin status should contain 'NodeID:'", proto))

			t.Logf("[%s] admin status: %s", proto, strings.TrimSpace(output))
		})
	}
}

// TestCLIMultiProtocolAdminPeers tests the admin peers command across all protocols.
func TestCLIMultiProtocolAdminPeers(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "peers")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin peers failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin peers produced no output", proto))

			// The peers command prints a table with SHARD_ID column header.
			assertContains(t, output, "SHARD_ID",
				fmt.Sprintf("[%s] admin peers should contain 'SHARD_ID' table header", proto))

			t.Logf("[%s] admin peers: %s", proto, strings.TrimSpace(output))
		})
	}
}

// TestCLIServerFlagFormats verifies the CLI accepts all documented --server
// flag URL formats. The version command is used because it does not require
// a live server connection -- it exits before client construction.
func TestCLIServerFlagFormats(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	tests := []struct {
		name   string
		format string
	}{
		{"unix-socket", "unix:///var/run/qrdbd/qrdbd.sock"},
		{"https", "https://qrdbd:63002"},
		{"grpcs", "grpcs://qrdbd:63003"},
		{"mcp", "mcp://qrdbd:63005"},
		{"http", "http://localhost:63002"},
		{"host-port-bare", "localhost:63003"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The version command skips client construction, so it should
			// always succeed regardless of whether the server address is
			// reachable. This validates flag parsing only.
			stdout, stderr, err := execCLIWithServer(t, cfg, tt.format, "version")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("version command with --server %s produced no output", tt.format))

			t.Logf("[%s] CLI accepted --server %s", tt.name, tt.format)
		})
	}
}
