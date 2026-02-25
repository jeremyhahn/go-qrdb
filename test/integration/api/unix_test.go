// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/integration/api/commands"
)

// TestUnixSocketHealth tests health via Unix socket using the CLI.
func TestUnixSocketHealth(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !isUnixSocketAvailable(t, cfg) {
		t.Fatal("Unix socket server not available")
	}

	runner := commands.NewTestRunner()
	stdout, stderr, err := runner.RunCommandWithProtocol(t, commands.ProtocolUnix, "admin", "health")
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("Unix socket admin health failed: %v", err)
	}

	output := stdout + stderr
	assertContains(t, output, "Status", "Unix health should contain Status")
	t.Logf("Unix socket health check succeeded: %s", output)
}

// TestUnixSocketKVRoundTrip tests KV operations via Unix socket CLI.
func TestUnixSocketKVRoundTrip(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !isUnixSocketAvailable(t, cfg) {
		t.Fatal("Unix socket server not available")
	}

	runner := commands.NewTestRunner()
	key := generateUniqueID("unix-test")
	value := "unix-test-value"

	// Put
	stdout, stderr, err := runner.RunCommandWithProtocol(t, commands.ProtocolUnix, "kv", "put", key, value)
	if err != nil {
		t.Logf("stdout: %s, stderr: %s", stdout, stderr)
		t.Fatalf("Unix put failed: %v", err)
	}

	// Get
	stdout, stderr, err = runner.RunCommandWithProtocol(t, commands.ProtocolUnix, "kv", "get", key)
	if err != nil {
		t.Logf("stdout: %s, stderr: %s", stdout, stderr)
		t.Fatalf("Unix get failed: %v", err)
	}
	assertContains(t, stdout, value, "Unix get should return stored value")

	// Delete
	_, _, err = runner.RunCommandWithProtocol(t, commands.ProtocolUnix, "kv", "del", key)
	assertNoError(t, err, "Unix delete failed")

	t.Logf("Unix socket KV round-trip succeeded for key: %s", key)
}
