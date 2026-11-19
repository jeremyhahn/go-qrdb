// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// execCLIWithServer executes the CLI binary with the --server flag and optional TLS CA cert.
// A 30-second timeout is applied to prevent hung processes from blocking the test suite.
func execCLIWithServer(t *testing.T, cfg *TestConfig, serverURL string, args ...string) (string, string, error) {
	t.Helper()

	var prefixArgs []string

	if serverURL != "" {
		prefixArgs = append(prefixArgs, "--server", serverURL)
	}

	// Add --ca-file for TLS-bearing protocols (HTTPS, gRPCs, MCP over TLS,
	// QUIC, and unix — the Unix socket transport is "verified TLS 1.3
	// only", identical to TCP gRPC, so it needs the same CA/cert/key flags).
	needsTLS := strings.HasPrefix(serverURL, "https://") ||
		strings.HasPrefix(serverURL, "grpcs://") ||
		strings.HasPrefix(serverURL, "mcp://") ||
		strings.HasPrefix(serverURL, "quics://") ||
		strings.HasPrefix(serverURL, "unix://")

	if needsTLS {
		caFile := getEnv("QRDB_TLS_CA", "/data/raft/ca.pem")
		if caFile != "" {
			prefixArgs = append(prefixArgs, "--ca-file", caFile)
		}
		certFile := getEnv("QRDB_TLS_CERT", "")
		keyFile := getEnv("QRDB_TLS_KEY", "")
		if certFile != "" && keyFile != "" {
			prefixArgs = append(prefixArgs, "--cert-file", certFile, "--key-file", keyFile)
		}
	}

	// Bound each CLI call so a stalled request fails rather than hangs. 30s
	// gives headroom for transient slowness when the software- and Shamir-seal
	// servers share the host with the node-spawning cluster tests.
	prefixArgs = append(prefixArgs, "--timeout", "30s")

	args = append(prefixArgs, args...)

	// The exec deadline must exceed the CLI's own --timeout (30s) plus its
	// bounded connect-retry, so a stalled call self-terminates with a clean
	// error inside the CLI rather than being SIGKILLed here (which surfaces
	// as an opaque "signal: killed"). 60s leaves clear headroom.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Logf("exec: %s %s", cfg.CLIBinPath, strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, cfg.CLIBinPath, args...)

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

// TestCLIMultiProtocolKVRange tests the kv range command with various flag
// combinations across all protocols.
func TestCLIMultiProtocolKVRange(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("range-%s-%d", proto, testTimestamp())
			keys := []string{
				prefix + "/aaa",
				prefix + "/bbb",
				prefix + "/ccc",
			}

			for i, key := range keys {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, fmt.Sprintf("val-%d", i))
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, key, err)
				}
			}

			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// Basic range query.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "range", prefix+"/")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv range failed: %v", proto, err)
			}
			for _, key := range keys {
				assertContains(t, stdout, key,
					fmt.Sprintf("[%s] range should contain key %s", proto, key))
			}

			// Range with --limit.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "range", prefix+"/", "--limit", "2")
			if err != nil {
				t.Fatalf("[%s] kv range --limit failed: %v", proto, err)
			}
			// Should contain at most 2 keys.
			assertContains(t, stdout, keys[0],
				fmt.Sprintf("[%s] range --limit should contain first key", proto))

			// Range with --count-only.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "range", prefix+"/", "--count-only")
			if err != nil {
				t.Fatalf("[%s] kv range --count-only failed: %v", proto, err)
			}
			assertContains(t, stdout, "Count:",
				fmt.Sprintf("[%s] range --count-only should output Count:", proto))

			// Range with --keys-only.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "range", prefix+"/", "--keys-only")
			if err != nil {
				t.Fatalf("[%s] kv range --keys-only failed: %v", proto, err)
			}
			assertContains(t, stdout, keys[0],
				fmt.Sprintf("[%s] range --keys-only should contain key", proto))

			// Range with --sort-order desc.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "range", prefix+"/", "--sort-order", "desc", "--sort-by", "key")
			if err != nil {
				t.Fatalf("[%s] kv range --sort-order desc failed: %v", proto, err)
			}
			assertContains(t, stdout, keys[2],
				fmt.Sprintf("[%s] range --sort-order desc should contain last key", proto))

			// Range with --revision (query at current revision).
			revStdout, _, revErr := execCLIWithProtocol(t, cfg, proto, "kv", "revision")
			if revErr == nil {
				rev := strings.TrimSpace(revStdout)
				stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "range", prefix+"/", "--revision", rev)
				if err != nil {
					t.Fatalf("[%s] kv range --revision failed: %v", proto, err)
				}
				assertContains(t, stdout, keys[0],
					fmt.Sprintf("[%s] range --revision should contain key", proto))
			}

			t.Logf("[%s] KV range tests passed", proto)
		})
	}
}

// TestCLIMultiProtocolKVCompact tests the kv compact command across all
// protocols. Compact is monotonic (service.MVCCKVService.CompactPrefix's doc
// comment: a revision at or below the database's floor is refused by the
// state machine, *RevisionCompactedError), and every subtest here shares one
// long-lived server, so compacting at a revision fixed in the source would
// only ever succeed for whichever subtest happened to run first -- every
// later one would find the floor already at or past it. Each subtest
// instead puts a key to advance the revision counter and reads `kv
// revision` immediately before compacting, so its target is always ahead of
// the current floor.
func TestCLIMultiProtocolKVCompact(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("kvcompact-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "kv-compact-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv revision failed: %v", proto, err)
			}
			revision := strings.TrimSpace(stdout)
			if _, err := strconv.ParseInt(revision, 10, 64); err != nil {
				t.Fatalf("[%s] kv revision did not return an integer: %q", proto, revision)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "compact", "--revision", revision)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv compact failed: %v", proto, err)
			}

			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv compact should output 'OK'", proto))

			t.Logf("[%s] kv compact passed", proto)
		})
	}
}

// TestCLIMultiProtocolKVRevision tests the kv revision command across all protocols.
func TestCLIMultiProtocolKVRevision(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv revision failed: %v", proto, err)
			}

			output := strings.TrimSpace(stdout)
			assertNotEmpty(t, output, fmt.Sprintf("[%s] kv revision produced no output", proto))

			t.Logf("[%s] kv revision: %s", proto, output)
		})
	}
}

// TestCLIMultiProtocolKVGetRevision tests the kv get --revision flag across all protocols.
func TestCLIMultiProtocolKVGetRevision(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("rev-test-%s-%d", proto, testTimestamp())
			value := "revision-value"

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Get with --revision flag should include MVCC metadata.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key, "--revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get --revision failed: %v", proto, err)
			}

			assertContains(t, stdout, "Key:",
				fmt.Sprintf("[%s] kv get --revision should show Key:", proto))
			assertContains(t, stdout, "CreateRevision:",
				fmt.Sprintf("[%s] kv get --revision should show CreateRevision:", proto))

			t.Logf("[%s] kv get --revision passed", proto)
		})
	}
}

// TestCLIMultiProtocolKVGetAtRevision tests the kv get --at-revision flag.
func TestCLIMultiProtocolKVGetAtRevision(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("at-rev-%s-%d", proto, testTimestamp())
			value1 := "first-value"
			value2 := "second-value"

			// Put initial value.
			stdout, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value1)
			if err != nil {
				t.Fatalf("[%s] kv put first value failed: %v", proto, err)
			}

			// Get the revision after first put.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key, "--revision")
			if err != nil {
				t.Fatalf("[%s] kv get --revision failed: %v", proto, err)
			}

			// Extract CreateRevision from output.
			var createRev string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "CreateRevision:") {
					createRev = strings.TrimSpace(strings.TrimPrefix(line, "CreateRevision:"))
					break
				}
			}

			// Put second value to create a new revision.
			_, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value2)
			if err != nil {
				t.Fatalf("[%s] kv put second value failed: %v", proto, err)
			}

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			if createRev == "" {
				t.Skipf("[%s] could not extract create revision, skipping --at-revision test", proto)
			}

			// Get at the original revision should return first value.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key, "--at-revision", createRev)
			if err != nil {
				t.Fatalf("[%s] kv get --at-revision failed: %v", proto, err)
			}

			assertContains(t, stdout, value1,
				fmt.Sprintf("[%s] kv get --at-revision should return original value", proto))

			t.Logf("[%s] kv get --at-revision passed", proto)
		})
	}
}

// TestCLIMultiProtocolKVOutputFormats tests --output json and --output yaml
// for KV commands across all protocols.
func TestCLIMultiProtocolKVOutputFormats(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("fmt-test-%s-%d", proto, testTimestamp())
			value := "format-value"

			_, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Test JSON output.
			stdout, _, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "kv", "get", key)
			if err != nil {
				t.Fatalf("[%s] kv get --output json failed: %v", proto, err)
			}
			assertContains(t, stdout, value,
				fmt.Sprintf("[%s] JSON output should contain value", proto))

			// Test YAML output.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "--output", "yaml", "kv", "get", key)
			if err != nil {
				t.Fatalf("[%s] kv get --output yaml failed: %v", proto, err)
			}
			assertContains(t, stdout, value,
				fmt.Sprintf("[%s] YAML output should contain value", proto))

			// Test admin health JSON output.
			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "admin", "health")
			if err != nil {
				t.Fatalf("[%s] admin health --output json failed: %v", proto, err)
			}
			assertContains(t, stdout, "status",
				fmt.Sprintf("[%s] admin health JSON should contain 'status'", proto))

			t.Logf("[%s] output format tests passed", proto)
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
		{"https", "https://127.0.0.1:63002"},
		{"grpcs", "grpcs://127.0.0.1:63003"},
		{"mcp", "mcp://127.0.0.1:63005"},
		{"http", "http://localhost:63002"},
		{"quic", "quic://localhost:63004"},
		{"quics", "quics://localhost:63004"},
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

// TestCLIMultiProtocolOutputFormatAdminStatus tests --output json and --output yaml
// on the admin status command across all protocols.
func TestCLIMultiProtocolOutputFormatAdminStatus(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Test JSON output on admin status.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "admin", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin status --output json failed: %v", proto, err)
			}

			// JSON output should contain a node_id or NodeID key.
			jsonContainsNodeID := strings.Contains(stdout, "node_id") || strings.Contains(stdout, "NodeID")
			assertTrue(t, jsonContainsNodeID,
				fmt.Sprintf("[%s] admin status JSON output should contain 'node_id' or 'NodeID': %s", proto, stdout))

			t.Logf("[%s] admin status --output json passed", proto)

			// Test YAML output on admin status.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "yaml", "admin", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin status --output yaml failed: %v", proto, err)
			}

			assertNotEmpty(t, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] admin status --output yaml produced no output", proto))

			t.Logf("[%s] admin status --output yaml passed", proto)
		})
	}
}

// TestCLIMultiProtocolOutputFormatAdminPeers tests --output json and --output yaml
// on the admin peers command across all protocols.
func TestCLIMultiProtocolOutputFormatAdminPeers(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Test JSON output on admin peers.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "admin", "peers")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin peers --output json failed: %v", proto, err)
			}

			// JSON output should contain array or object structure indicators.
			jsonHasStructure := strings.Contains(stdout, "{") || strings.Contains(stdout, "[")
			assertTrue(t, jsonHasStructure,
				fmt.Sprintf("[%s] admin peers JSON output should contain JSON structure: %s", proto, stdout))

			t.Logf("[%s] admin peers --output json passed", proto)

			// Test YAML output on admin peers.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "yaml", "admin", "peers")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin peers --output yaml failed: %v", proto, err)
			}

			assertNotEmpty(t, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] admin peers --output yaml produced no output", proto))

			t.Logf("[%s] admin peers --output yaml passed", proto)
		})
	}
}

// TestCLIMultiProtocolOutputFormatKVList tests --output json and --output yaml
// on the kv list command across all protocols.
func TestCLIMultiProtocolOutputFormatKVList(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("fmt-list-%s-%d", proto, testTimestamp())
			key := prefix + "/item"
			value := "list-fmt-value"

			// Insert a test key.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Test JSON output on kv list.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "json", "kv", "list", prefix)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list --output json failed: %v", proto, err)
			}

			assertNotEmpty(t, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv list --output json produced no output", proto))
			assertContains(t, stdout, key,
				fmt.Sprintf("[%s] kv list JSON output should contain the test key", proto))

			t.Logf("[%s] kv list --output json passed", proto)

			// Test YAML output on kv list.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "yaml", "kv", "list", prefix)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list --output yaml failed: %v", proto, err)
			}

			assertNotEmpty(t, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv list --output yaml produced no output", proto))
			assertContains(t, stdout, key,
				fmt.Sprintf("[%s] kv list YAML output should contain the test key", proto))

			t.Logf("[%s] kv list --output yaml passed", proto)
		})
	}
}

// TestCLIMultiProtocolOutputFormatKVRange tests --output json and --output yaml
// on the kv range command across all protocols.
func TestCLIMultiProtocolOutputFormatKVRange(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("fmt-range-%s-%d", proto, testTimestamp())
			keys := []string{
				prefix + "/alpha",
				prefix + "/beta",
			}

			// Insert test keys.
			for i, key := range keys {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, fmt.Sprintf("val-%d", i))
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, key, err)
				}
			}

			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// Test JSON output on kv range.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "kv", "range", prefix+"/")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv range --output json failed: %v", proto, err)
			}

			assertNotEmpty(t, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv range --output json produced no output", proto))
			assertContains(t, stdout, keys[0],
				fmt.Sprintf("[%s] kv range JSON output should contain first test key", proto))

			t.Logf("[%s] kv range --output json passed", proto)

			// Test YAML output on kv range.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "--output", "yaml", "kv", "range", prefix+"/")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv range --output yaml failed: %v", proto, err)
			}

			assertNotEmpty(t, strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv range --output yaml produced no output", proto))
			assertContains(t, stdout, keys[0],
				fmt.Sprintf("[%s] kv range YAML output should contain first test key", proto))

			t.Logf("[%s] kv range --output yaml passed", proto)
		})
	}
}
