// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolTxCompareAndSwap tests a successful transaction where the
// compare condition matches and the success operations are executed.
func TestCLIMultiProtocolTxCompareAndSwap(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("tx-key-%s-%d", proto, testTimestamp())
			originalValue := "original"
			swappedValue := "swapped"

			// Step 1: Put a key with a known value.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, originalValue)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put should output 'OK'", proto))

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Step 2: Build transaction JSON with base64-encoded keys and values.
			b64Key := base64.StdEncoding.EncodeToString([]byte(key))
			b64Original := base64.StdEncoding.EncodeToString([]byte(originalValue))
			b64Swapped := base64.StdEncoding.EncodeToString([]byte(swappedValue))

			txJSON := fmt.Sprintf(`{
  "compares": [
    {"key": "%s", "target": 3, "result": 0, "value": "%s"}
  ],
  "success_ops": [
    {"type": 0, "key": "%s", "value": "%s"}
  ],
  "failure_ops": []
}`, b64Key, b64Original, b64Key, b64Swapped)

			txFile := createTempJSONFile(t, txJSON)

			// Step 3: Execute the transaction.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "tx", "execute", "--file", txFile)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tx execute failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "Succeeded: true",
				fmt.Sprintf("[%s] tx execute should report 'Succeeded: true'", proto))

			// Step 4: Verify the key now has the swapped value.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get after tx failed: %v", proto, err)
			}
			assertContains(t, strings.TrimSpace(stdout), swappedValue,
				fmt.Sprintf("[%s] kv get should return swapped value after successful tx", proto))

			t.Logf("[%s] tx compare-and-swap passed for key: %s", proto, key)
		})
	}
}

// TestCLIMultiProtocolTxCompareAndSwapFailure tests a transaction where the
// compare condition does not match, causing the failure operations to execute.
func TestCLIMultiProtocolTxCompareAndSwapFailure(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			key := fmt.Sprintf("tx-fail-%s-%d", proto, testTimestamp())
			actualValue := "actual"
			wrongValue := "wrong-value"
			shouldNotHappen := "should-not-happen"
			failureBranch := "failure-branch"

			// Step 1: Put a key with a known value.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, actualValue)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put should output 'OK'", proto))

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Step 2: Build transaction JSON comparing against a wrong value.
			b64Key := base64.StdEncoding.EncodeToString([]byte(key))
			b64Wrong := base64.StdEncoding.EncodeToString([]byte(wrongValue))
			b64ShouldNot := base64.StdEncoding.EncodeToString([]byte(shouldNotHappen))
			b64Failure := base64.StdEncoding.EncodeToString([]byte(failureBranch))

			txJSON := fmt.Sprintf(`{
  "compares": [
    {"key": "%s", "target": 3, "result": 0, "value": "%s"}
  ],
  "success_ops": [
    {"type": 0, "key": "%s", "value": "%s"}
  ],
  "failure_ops": [
    {"type": 0, "key": "%s", "value": "%s"}
  ]
}`, b64Key, b64Wrong, b64Key, b64ShouldNot, b64Key, b64Failure)

			txFile := createTempJSONFile(t, txJSON)

			// Step 3: Execute the transaction (should fail the compare).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "tx", "execute", "--file", txFile)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tx execute failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "Succeeded: false",
				fmt.Sprintf("[%s] tx execute should report 'Succeeded: false'", proto))

			// Step 4: Verify the failure branch was executed.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get after failed tx failed: %v", proto, err)
			}
			assertContains(t, strings.TrimSpace(stdout), failureBranch,
				fmt.Sprintf("[%s] kv get should return failure-branch value after failed tx", proto))

			t.Logf("[%s] tx compare-and-swap failure passed for key: %s", proto, key)
		})
	}
}
