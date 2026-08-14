// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// execCLIWithStdin executes the CLI binary with the --server flag, optional
// TLS CA cert, and pipes the provided string to the command's standard input.
func execCLIWithStdin(t *testing.T, cfg *TestConfig, proto ProtocolType, stdin string, args ...string) (string, string, error) {
	t.Helper()

	serverURL := cfg.GetServerURL(proto)
	var prefixArgs []string
	if serverURL != "" {
		prefixArgs = append(prefixArgs, "--server", serverURL)
	}

	// Unix (grpc-over-UDS) is "verified TLS 1.3 only", identical to TCP
	// gRPC, so it needs the same CA/cert/key flags as the other transports.
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

	args = append(prefixArgs, args...)
	cmd := exec.Command(cfg.CLIBinPath, args...)
	cmd.Stdin = strings.NewReader(stdin)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	return stdout.String(), stderr.String(), err
}

// TestCLIMultiProtocolBatchExecuteFile tests batch execute from a JSON file
// across all protocols. It creates a temporary file containing three put
// operations with base64-encoded keys and values, executes the batch, and
// verifies each key was written successfully.
func TestCLIMultiProtocolBatchExecuteFile(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("batch-file-%s-%d", proto, testTimestamp())
			keys := []string{
				prefix + "/alpha",
				prefix + "/beta",
				prefix + "/gamma",
			}
			values := []string{
				"value-alpha",
				"value-beta",
				"value-gamma",
			}

			// Build batch JSON with base64-encoded keys and values.
			var ops []string
			for i, key := range keys {
				ops = append(ops, fmt.Sprintf(
					`{"type":0,"key":"%s","value":"%s"}`,
					base64.StdEncoding.EncodeToString([]byte(key)),
					base64.StdEncoding.EncodeToString([]byte(values[i])),
				))
			}
			batchJSON := fmt.Sprintf(`{"ops":[%s]}`, strings.Join(ops, ","))

			filePath := createTempJSONFile(t, batchJSON)

			// Cleanup inserted keys after test.
			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// Execute batch from file.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "batch", "execute", "--file", filePath)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] batch execute --file failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "succeeded=true",
				fmt.Sprintf("[%s] batch execute output should contain 'succeeded=true'", proto))

			// Verify each key was written.
			for i, key := range keys {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv get %s failed after batch: %v", proto, key, err)
				}
				assertContains(t, strings.TrimSpace(stdout), values[i],
					fmt.Sprintf("[%s] kv get should return value for key %s", proto, key))
			}

			t.Logf("[%s] batch execute --file passed for %d operations", proto, len(keys))
		})
	}
}

// TestCLIMultiProtocolBatchExecuteStdin tests batch execute from standard
// input across all protocols. It pipes a JSON payload containing two put
// operations with base64-encoded keys and values to the CLI's stdin, then
// verifies each key was written successfully.
func TestCLIMultiProtocolBatchExecuteStdin(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("batch-stdin-%s-%d", proto, testTimestamp())
			keys := []string{
				prefix + "/one",
				prefix + "/two",
			}
			values := []string{
				"stdin-value-one",
				"stdin-value-two",
			}

			// Build batch JSON with base64-encoded keys and values.
			var ops []string
			for i, key := range keys {
				ops = append(ops, fmt.Sprintf(
					`{"type":0,"key":"%s","value":"%s"}`,
					base64.StdEncoding.EncodeToString([]byte(key)),
					base64.StdEncoding.EncodeToString([]byte(values[i])),
				))
			}
			batchJSON := fmt.Sprintf(`{"ops":[%s]}`, strings.Join(ops, ","))

			// Cleanup inserted keys after test.
			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// Execute batch from stdin (--file - reads from stdin).
			stdout, stderr, err := execCLIWithStdin(t, cfg, proto, batchJSON, "batch", "execute", "--file", "-")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] batch execute via stdin failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "succeeded=true",
				fmt.Sprintf("[%s] batch execute stdin output should contain 'succeeded=true'", proto))

			// Verify each key was written.
			for i, key := range keys {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv get %s failed after batch stdin: %v", proto, key, err)
				}
				assertContains(t, strings.TrimSpace(stdout), values[i],
					fmt.Sprintf("[%s] kv get should return value for key %s", proto, key))
			}

			t.Logf("[%s] batch execute stdin passed for %d operations", proto, len(keys))
		})
	}
}
