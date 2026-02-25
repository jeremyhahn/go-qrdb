// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/integration/api/commands"
)

// TestGRPCProtocolHealth tests health via gRPC using the CLI.
func TestGRPCProtocolHealth(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !isGRPCServerAvailable(t, cfg) {
		t.Fatal("gRPC server not available")
	}

	runner := commands.NewTestRunner()
	stdout, stderr, err := runner.RunCommandWithProtocol(t, commands.ProtocolGRPC, "admin", "health")
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("gRPC admin health failed: %v", err)
	}

	output := stdout + stderr
	assertContains(t, output, "Status", "gRPC health should contain Status")
	t.Logf("gRPC health check succeeded: %s", output)
}

// TestGRPCProtocolKVRoundTrip tests KV operations via gRPC CLI.
func TestGRPCProtocolKVRoundTrip(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	if !isGRPCServerAvailable(t, cfg) {
		t.Fatal("gRPC server not available")
	}

	runner := commands.NewTestRunner()
	key := generateUniqueID("grpc-test")
	value := "grpc-test-value"

	// Put
	stdout, stderr, err := runner.RunCommandWithProtocol(t, commands.ProtocolGRPC, "kv", "put", key, value)
	if err != nil {
		t.Logf("stdout: %s, stderr: %s", stdout, stderr)
		t.Fatalf("gRPC put failed: %v", err)
	}

	// Get
	stdout, stderr, err = runner.RunCommandWithProtocol(t, commands.ProtocolGRPC, "kv", "get", key)
	if err != nil {
		t.Logf("stdout: %s, stderr: %s", stdout, stderr)
		t.Fatalf("gRPC get failed: %v", err)
	}
	assertContains(t, stdout, value, "gRPC get should return stored value")

	// Delete
	_, _, err = runner.RunCommandWithProtocol(t, commands.ProtocolGRPC, "kv", "del", key)
	assertNoError(t, err, "gRPC delete failed")

	t.Logf("gRPC KV round-trip succeeded for key: %s", key)
}
