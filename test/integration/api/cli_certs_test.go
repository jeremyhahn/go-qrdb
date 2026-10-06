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
// (issue, list, revoke) across all protocols. Server-side keygen is
// retired: "ca issue" signs a CSR for an EXISTING account of the caller's
// own tenant, so the account is created first and the CSR comes from
// "ca csr" (the --csr form; no private key reaches the server).
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

			// Step 0: the account must exist before a certificate can be
			// issued for it.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"users", "create", "--without-email-login-key", "--username", username)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create failed: %v", proto, err)
			}

			// Step 1: generate a CSR locally -- no private key ever reaches
			// the server.
			csrDir := t.TempDir()
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "csr", "--username", username, "--out", csrDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca csr failed: %v", proto, err)
			}
			csrFile := filepath.Join(csrDir, "csr.pem")

			// Step 2: issue a certificate from that CSR.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", username, "--csr", csrFile, "--ttl", "8760h")
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

			// Step 3: list certificates and verify the output is not empty.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "ca", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca list failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] ca list should produce output", proto))

			// Step 4: revoke the certificate.
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

// TestCLIMultiProtocolCertsExport exercises "ca issue" over every protocol,
// using the --out form (no --csr: a fresh key is generated locally and
// written beside the certificate), then "ca export-bundle" over the
// resulting PEM pair. Export-bundle itself makes no server call at all --
// no node may receive a private key to encode on a caller's behalf
// (go-qrdb security review) -- so this proves issuance over each protocol
// still hands the CLI a pair the LOCAL PKCS#12 build accepts.
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

			// Step 0: the account must exist before a certificate can be
			// issued for it.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"users", "create", "--without-email-login-key", "--username", username)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create failed: %v", proto, err)
			}

			// Step 1: issue a certificate, generating its key locally, and
			// write both to disk.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
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

			// Step 2: export the certificate as a PKCS#12 bundle.
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

			// Step 3: revoke the certificate to clean up.
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

// TestCLIMultiProtocolCertsIssueOwnTenantOnly replaces the old
// --tenant-id-based cross-tenant generation test: --tenant-id no longer
// exists on "ca issue" (the tenant is always the caller's own, never an
// argument), so what this now proves is:
//
//  1. the system tenant's harness admin cannot issue a certificate for an
//     account that lives only in another tenant -- the account does not
//     exist in the admin's OWN tenant's namespace, so the CSR is refused as
//     one for a nonexistent account;
//  2. a tenant's own administrator CANNOT issue a certificate for an
//     account that does not exist in ITS tenant either (a CSR for a
//     nonexistent account is refused regardless of caller);
//  3. a tenant's own administrator CAN issue, list and revoke a
//     certificate for an account that does exist in its own tenant (itself).
func TestCLIMultiProtocolCertsIssueOwnTenantOnly(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireCertService(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("cert-tenant-%s-%d", proto, ts)
			admin := newTenantAdminIdentity(t, cfg, proto, tenantID,
				"certs:issue", "certs:list", "certs:revoke")

			csrDir := t.TempDir()
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ca", "csr", "--username", admin.username, "--out", csrDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca csr failed: %v", proto, err)
			}
			csrFile := filepath.Join(csrDir, "csr.pem")

			// Step 1: the system tenant's admin may not issue a certificate
			// for an account that exists only in tenantID: in the system
			// tenant's own namespace, no such account exists.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", admin.username, "--csr", csrFile)
			if err == nil {
				t.Fatalf("[%s] system admin ca issue for %s should fail (no such account in the system tenant); stdout: %s", proto, admin.username, stdout)
			}

			// Step 2: the tenant's own administrator may not issue a
			// certificate for an account that does not exist in ITS tenant
			// either -- a CSR for a nonexistent account is refused.
			bogusUsername := fmt.Sprintf("no-such-user-%s-%d", proto, ts)
			bogusCSRDir := t.TempDir()
			stdout, stderr, err = admin.exec(t, cfg, proto,
				"ca", "csr", "--username", bogusUsername, "--out", bogusCSRDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca csr for the bogus account failed: %v", proto, err)
			}
			stdout, stderr, err = admin.exec(t, cfg, proto,
				"ca", "issue", "--username", bogusUsername, "--csr", filepath.Join(bogusCSRDir, "csr.pem"))
			if err == nil {
				t.Fatalf("[%s] tenant admin ca issue for a nonexistent account (%s) should fail; stdout: %s", proto, bogusUsername, stdout)
			}

			// Step 3: the tenant's own administrator issues a certificate
			// for an account that DOES exist in its own tenant (itself).
			stdout, stderr, err = admin.exec(t, cfg, proto,
				"ca", "issue", "--username", admin.username, "--csr", csrFile, "--ttl", "8760h")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant admin ca issue failed: %v", proto, err)
			}
			assertContains(t, stdout, "Serial:",
				fmt.Sprintf("[%s] ca issue should contain 'Serial:'", proto))

			var serial string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "Serial:") {
					serial = strings.TrimSpace(strings.TrimPrefix(line, "Serial:"))
					break
				}
			}
			assertNotEmpty(t, serial,
				fmt.Sprintf("[%s] ca issue should produce a serial number", proto))

			t.Cleanup(func() {
				if serial != "" {
					admin.exec(t, cfg, proto, "ca", "revoke", "--serial", serial) //nolint:errcheck
				}
			})

			// Step 4: the system tenant's admin may not list or revoke the
			// tenant's certificates.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "ca", "list", "--tenant-id", tenantID)
			assertPermissionDenied(t, proto, "system admin ca list --tenant-id "+tenantID, stdout, stderr, err)
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "ca", "revoke", "--serial", serial)
			assertPermissionDenied(t, proto, "system admin ca revoke of "+tenantID+"'s serial", stdout, stderr, err)

			// Step 5: the tenant's administrator lists and finds the certificate.
			stdout, stderr, err = admin.exec(t, cfg, proto, "ca", "list", "--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant admin ca list --tenant-id failed: %v", proto, err)
			}
			assertContains(t, stdout, serial,
				fmt.Sprintf("[%s] ca list --tenant-id should list the issued serial", proto))

			// Step 6: the tenant's administrator revokes it.
			stdout, stderr, err = admin.exec(t, cfg, proto, "ca", "revoke", "--serial", serial)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant admin ca revoke failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] ca revoke should output 'OK'", proto))
			serial = ""

			t.Logf("[%s] ca issue own-tenant-only passed for admin: %s", proto, admin.username)
		})
	}
}
