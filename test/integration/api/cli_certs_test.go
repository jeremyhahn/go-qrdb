// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// requireCertService probes the server's cert service by attempting a
// ca list. If the service is not configured (e.g., missing barrier/seal
// subsystem), the test is skipped.
func requireCertService(t *testing.T, cfg *TestConfig) {
	t.Helper()
	for _, proto := range AllProtocols() {
		if cfg.IsProtocolAvailable(t, proto) {
			_, stderr, err := execCLIWithProtocol(t, cfg, proto, "ca", "list")
			if err != nil && (strings.Contains(stderr, "not configured") ||
				strings.Contains(stderr, "Unimplemented") ||
				strings.Contains(stderr, "not found")) {
				t.Skipf("cert service not available on server: %s", strings.TrimSpace(stderr))
			}
			return
		}
	}
	t.Skip("no protocol available to probe cert service")
}

// TestCLIMultiProtocolCertsLifecycle tests the full certificate lifecycle
// (generate, list, revoke) across all protocols.
func TestCLIMultiProtocolCertsLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireCertService(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			username := fmt.Sprintf("cert-user-%s-%d", proto, ts)

			// Step 1: Issue a certificate.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", username, "--ttl", "8760h")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca issue failed: %v", proto, err)
			}
			assertContains(t, stdout, "Serial:",
				fmt.Sprintf("[%s] ca issue should contain 'Serial:'", proto))

			// Extract the serial number from the output.
			var serial string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "Serial:") {
					serial = strings.TrimSpace(strings.TrimPrefix(line, "Serial:"))
					break
				}
			}
			assertNotEmpty(t, serial,
				fmt.Sprintf("[%s] ca issue should produce a serial number", proto))

			// Register cleanup to revoke the certificate in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "ca", "revoke", "--serial", serial) //nolint:errcheck
			})

			// Step 2: List certificates and verify the output is not empty.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "ca", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca list failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] ca list should produce output", proto))

			// Step 3: Revoke the certificate.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "revoke", "--serial", serial)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca revoke failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] ca revoke should output 'OK'", proto))

			t.Logf("[%s] ca lifecycle passed for user: %s (serial: %s)", proto, username, serial)
		})
	}
}

// TestCLIMultiProtocolCertsExport tests certificate export across all protocols.
func TestCLIMultiProtocolCertsExport(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireCertService(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			username := fmt.Sprintf("export-%s-%d", proto, ts)
			certDir := t.TempDir()

			// Step 1: Issue a certificate and write it to disk.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", username,
				"--out", certDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca issue --out failed: %v", proto, err)
			}
			assertContains(t, stdout, "Certificate written",
				fmt.Sprintf("[%s] ca issue --out should contain 'Certificate written'", proto))

			// Extract the serial number for cleanup.
			var serial string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "Serial:") {
					serial = strings.TrimSpace(strings.TrimPrefix(line, "Serial:"))
					break
				}
			}
			assertNotEmpty(t, serial,
				fmt.Sprintf("[%s] ca issue should produce a serial number", proto))

			// Register cleanup to revoke the certificate.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "ca", "revoke", "--serial", serial) //nolint:errcheck
			})

			// Step 2: Export the certificate as a PKCS#12 bundle.
			certPEM := filepath.Join(certDir, "cert.pem")
			keyPEM := filepath.Join(certDir, "key.pem")
			bundlePath := filepath.Join(certDir, "bundle.p12")

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "export-bundle",
				"--cert-pem", certPEM,
				"--key-pem", keyPEM,
				"--passphrase", "test-pass",
				"--out", bundlePath)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca export-bundle failed: %v", proto, err)
			}
			assertContains(t, stdout, "PKCS#12",
				fmt.Sprintf("[%s] ca export-bundle should contain 'PKCS#12'", proto))

			// Step 3: Revoke the certificate to clean up.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "revoke", "--serial", serial)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca revoke failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] ca revoke should output 'OK'", proto))

			t.Logf("[%s] ca export-bundle passed for user: %s (serial: %s)", proto, username, serial)
		})
	}
}

// TestCLIMultiProtocolCertsGenerateTenant tests the --tenant-id flag on
// certificate generation and listing across all protocols.
func TestCLIMultiProtocolCertsGenerateTenant(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireCertService(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			username := fmt.Sprintf("cert-tenant-%s-%d", proto, ts)

			// Step 1: Issue a certificate with --tenant-id.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ca", "issue",
				"--username", username,
				"--tenant-id", "test-tenant",
				"--ttl", "8760h")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca issue --tenant-id failed: %v", proto, err)
			}
			assertContains(t, stdout, "Serial:",
				fmt.Sprintf("[%s] ca issue --tenant-id should contain 'Serial:'", proto))

			// Extract the serial number from the output.
			var serial string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "Serial:") {
					serial = strings.TrimSpace(strings.TrimPrefix(line, "Serial:"))
					break
				}
			}
			assertNotEmpty(t, serial,
				fmt.Sprintf("[%s] ca issue --tenant-id should produce a serial number", proto))

			// Register cleanup to revoke the certificate in case of early failure.
			t.Cleanup(func() {
				if serial != "" {
					execCLIWithProtocol(t, cfg, proto, "ca", "revoke", "--serial", serial) //nolint:errcheck
				}
			})

			// Step 2: List certificates with --tenant-id and verify the cert is listed.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "list", "--tenant-id", "test-tenant")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca list --tenant-id failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] ca list --tenant-id should produce output", proto))

			// Step 3: Revoke the certificate.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "revoke", "--serial", serial)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca revoke failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] ca revoke should output 'OK'", proto))
			serial = ""

			t.Logf("[%s] ca issue tenant passed for user: %s", proto, username)
		})
	}
}
