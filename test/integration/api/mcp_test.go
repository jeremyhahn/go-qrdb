// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/integration/api/commands"
)

// TestMCPProtocolHealth tests health via MCP using the CLI.
func TestMCPProtocolHealth(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !isMCPServerAvailable(t, cfg) {
		t.Fatal("MCP server not available")
	}

	runner := commands.NewTestRunner()
	stdout, stderr, err := runner.RunCommandWithProtocol(t, commands.ProtocolMCP, "admin", "health")
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("MCP admin health failed: %v", err)
	}

	output := stdout + stderr
	assertContains(t, output, "Status", "MCP health should contain Status")
	t.Logf("MCP health check succeeded: %s", output)
}

// TestMCPProtocolKVRoundTrip tests KV operations via MCP CLI.
func TestMCPProtocolKVRoundTrip(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !isMCPServerAvailable(t, cfg) {
		t.Fatal("MCP server not available")
	}

	runner := commands.NewTestRunner()
	key := generateUniqueID("mcp-test")
	value := "mcp-test-value"

	// Put
	stdout, stderr, err := runner.RunCommandWithProtocol(t, commands.ProtocolMCP, "kv", "put", key, value)
	if err != nil {
		t.Logf("stdout: %s, stderr: %s", stdout, stderr)
		t.Fatalf("MCP put failed: %v", err)
	}

	// Get
	stdout, stderr, err = runner.RunCommandWithProtocol(t, commands.ProtocolMCP, "kv", "get", key)
	if err != nil {
		t.Logf("stdout: %s, stderr: %s", stdout, stderr)
		t.Fatalf("MCP get failed: %v", err)
	}
	assertContains(t, stdout, value, "MCP get should return stored value")

	// Delete
	_, _, err = runner.RunCommandWithProtocol(t, commands.ProtocolMCP, "kv", "del", key)
	assertNoError(t, err, "MCP delete failed")

	t.Logf("MCP KV round-trip succeeded for key: %s", key)
}
