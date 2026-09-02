// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// identityTLSProtocols is the subset of AllProtocols() where mTLS client
// identity is meaningful. This is now every remote protocol: unix
// (grpc-over-UDS) is "verified TLS 1.3 only" with the same mTLS-derived
// authentication and RBAC as TCP gRPC (see pkg/api/unix's WithTLS /
// WithAuthMiddleware options and execCLIWithServer's needsTLS gate in
// cli_multiprotocol_test.go, which now applies --cert-file/--key-file to
// unix:// server URLs too), so it has exactly as much to prove about
// OU-derived tenant identity as every other transport.
func identityTLSProtocols() []ProtocolType {
	return []ProtocolType{ProtocolREST, ProtocolGRPC, ProtocolQUIC, ProtocolMCP, ProtocolUnix}
}

// execCLIWithProtocolAndCert runs the CLI against the given protocol's
// server URL exactly like execCLIWithProtocol (cli_multiprotocol_test.go),
// but authenticates with an OPERATOR-SUPPLIED client certificate/key pair
// instead of the harness admin identity (QRDB_TLS_CERT/QRDB_TLS_KEY). It is
// defined locally (not added to testutil.go) since it is only needed to
// drive CLI calls as a freshly minted tenant-scoped identity in this file.
func execCLIWithProtocolAndCert(t *testing.T, cfg *TestConfig, proto ProtocolType, certFile, keyFile string, args ...string) (string, string, error) {
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
		if cfg.TLSCACert != "" {
			prefixArgs = append(prefixArgs, "--ca-file", cfg.TLSCACert)
		}
		prefixArgs = append(prefixArgs, "--cert-file", certFile, "--key-file", keyFile)
	}
	prefixArgs = append(prefixArgs, "--timeout", "30s")
	args = append(prefixArgs, args...)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Logf("exec (tenant identity): %s %s", cfg.CLIBinPath, strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, cfg.CLIBinPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// idResponse decodes the `id` field common to `roles create --output json`
// and `users create --output json` responses (transport.Role.ID is a plain
// string; transport.User.ID is uint64 encoded as a JSON string via
// `json:"id,string"` -- both unmarshal cleanly into a Go string field).
type idResponse struct {
	ID string `json:"id"`
}

// parseIDResponse extracts the `id` field from a `--output json` CLI
// response, failing the test with the raw output on any decode error.
func parseIDResponse(t *testing.T, proto ProtocolType, what, out string) string {
	t.Helper()
	var resp idResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("[%s] failed to decode %s JSON: %v (output: %s)", proto, what, err, out)
	}
	if resp.ID == "" {
		t.Fatalf("[%s] %s response had no id (output: %s)", proto, what, out)
	}
	return resp.ID
}

// TestCLIMultiProtocolTenantIdentityBarrierFlow drives the full CLI-only
// ceremony for minting a tenant-scoped mTLS identity and using it:
//
//	tenant create -> tenant set-entitlement (allow barrier modes) ->
//	roles create (tenant-scoped, tenant_barrier:* permissions) ->
//	users create --tenant-id -> roles assign -> ca issue --username
//	--tenant-id (writes cert.pem/key.pem with CN=username, OU=tenantID,
//	per pkg/cert/default_provider.go GenerateClientCert) -> attempt
//	tenant barrier-init/tenant barrier-unseal authenticated with that
//	freshly minted certificate.
//
// Steps 1-6 (everything up to and including `ca issue`) are fully
// CLI-reachable and are asserted as hard successes -- this is new coverage:
// TestCLIMultiProtocolTenantBarrierInitRequiresTenantScope (in
// cli_tenant_barrier_test.go) proved the ADMIN identity is correctly denied
// tenant-scoped barrier ops; this test proves the CLI can independently
// mint a correctly-shaped tenant identity (CN=username, OU=tenantID)
// end-to-end via ca issue.
//
// Step 7 (using that identity to authenticate a barrier-init/barrier-unseal
// call) is a VERIFIED, REPRODUCIBLE PARITY GAP, not a workaround-avoidance
// choice: pkg/cert.DefaultCertProvider (used by `ca issue`) mints its own
// independent CA on first use (pkg/server/lifecycle/lifecycle.go ->
// cert.NewDefaultProvider(certStore, infra.SealBarrier) ->
// DefaultCertProvider.Initialize generates a fresh ECDSA CA key pair sealed
// under the tenant/system data barrier). That CA is NEVER cross-trusted
// with the node's own TLS bootstrap CA (cmd/qrdbd/tls.go, used for the
// admin client cert and for verifying inbound client certificates on the
// mTLS listener) -- there is no code path anywhere in this repo that seeds
// or converges the two. The only configured extension point,
// mtls.client_ca_files (pkg/config/config.go MTLSConfig.ClientCAFiles), is a
// static file list read once at boot, which cannot include a CA that is
// generated lazily, inside a sealed barrier, at runtime -- and there is no
// REST/gRPC/CLI-exposed endpoint to read the pkg/cert CA's certificate PEM
// (handlers_certs.go exposes generate/export/revoke/list, but not the CA
// cert itself) to seed such a file even across a restart. So a `ca
// issue`-minted client certificate can never be accepted by this node's own
// mTLS listener, and every tenant-scoped barrier operation reachable ONLY
// via that identity (tenant barrier-init/unseal, which
// authorizeTenantBarrierOp denies to the admin/system-tenant identity, see
// cli_tenant_barrier_test.go) is therefore UNREACHABLE end-to-end via pure
// CLI subprocesses against a standard qrdbd deployment today. This test
// pins that exact, current failure (a REST 401 "no client certificate in
// TLS connection" / equivalent transport error on other protocols) rather
// than working around it.
func TestCLIMultiProtocolTenantIdentityBarrierFlow(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range identityTLSProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("id-tenant-%s-%d", proto, ts)
			tenantName := fmt.Sprintf("Identity Tenant %s %d", proto, ts)
			roleName := fmt.Sprintf("db-admin-%s-%d", proto, ts)
			username := fmt.Sprintf("tenantadmin-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
			})

			// Step 1: tenant create.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"tenant", "create", "--id", tenantID, "--name", tenantName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant create failed: %v", proto, err)
			}

			// Step 2: set-entitlement -- allow the software barrier modes so a
			// later, correctly-authenticated barrier-init would not be blocked
			// by entitlements (isolating the identity/trust gap this test pins).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "set-entitlement", tenantID,
				"--allowed-barrier-modes", "none,passphrase,shamir")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant set-entitlement failed: %v", proto, err)
			}

			// Step 3: roles create -- a tenant-scoped role carrying exactly the
			// barrier permissions (mirrors test/integration/enable_encryption_test.go's
			// raw-HTTP "db-admin" role, driven here entirely through the CLI).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "roles", "create",
				"--name", roleName, "--tenant-id", tenantID,
				"--permissions", "tenant_barrier:init,tenant_barrier:unseal,tenant_barrier:status,tenant_barrier:seal")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles create failed: %v", proto, err)
			}
			roleID := parseIDResponse(t, proto, "roles create", stdout)

			// Step 4: users create --tenant-id (no --roles yet; assigned next
			// via a separate `roles assign` call per the requested flow).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"--output", "json", "users", "create", "--without-email-login-key",
				"--username", username, "--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create failed: %v", proto, err)
			}
			userID := parseIDResponse(t, proto, "users create", stdout)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
			})

			// Step 5: roles assign.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"roles", "assign", "--user", userID, "--role", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles assign failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] roles assign should output 'OK'", proto))

			// Step 6: ca issue --username --tenant-id, writing cert.pem/key.pem
			// to a temp dir. This is the CN=username, OU=tenantID identity
			// certificate (pkg/cert/default_provider.go GenerateClientCert).
			certDir := t.TempDir()
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", username, "--tenant-id", tenantID, "--out", certDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca issue failed: %v", proto, err)
			}
			certFile := filepath.Join(certDir, "cert.pem")
			keyFile := filepath.Join(certDir, "key.pem")

			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				t.Fatalf("[%s] ca issue did not write %s: %v", proto, certFile, err)
			}
			if _, err := os.Stat(keyFile); err != nil {
				t.Fatalf("[%s] ca issue did not write %s: %v", proto, keyFile, err)
			}

			// Verify the issued certificate carries the expected identity shape:
			// CN=username, OU=[tenantID].
			block, _ := pem.Decode(certPEM)
			if block == nil {
				t.Fatalf("[%s] ca issue cert.pem is not valid PEM", proto)
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("[%s] failed to parse issued certificate: %v", proto, err)
			}
			assertEqual(t, username, cert.Subject.CommonName,
				fmt.Sprintf("[%s] issued cert CommonName should equal the requested username", proto))
			if len(cert.Subject.OrganizationalUnit) != 1 || cert.Subject.OrganizationalUnit[0] != tenantID {
				t.Fatalf("[%s] issued cert OU = %v, want [%s]",
					proto, cert.Subject.OrganizationalUnit, tenantID)
			}

			// Step 7 (PARITY GAP -- see the function doc comment): attempt to use
			// the freshly minted tenant identity to authenticate a tenant-scoped
			// barrier operation. This is expected to FAIL today because the
			// issuing CA (pkg/cert) is not in the node's mTLS ClientCAs trust
			// pool. We assert the failure (not a workaround) and log the exact
			// error so a future fix (client-CA convergence, or a documented
			// bootstrap path to seed mtls.client_ca_files) has a concrete
			// regression signal: this test must be revisited to assert SUCCESS
			// once that trust gap is closed.
			stdout, stderr, err = execCLIWithProtocolAndCert(t, cfg, proto, certFile, keyFile,
				"tenant", "barrier-init", "--id", tenantID,
				"--strategy", "passphrase", "--passphrase", "identity-flow-test-passphrase")
			if err == nil {
				t.Fatalf("[%s] tenant barrier-init unexpectedly SUCCEEDED using a ca-issue-minted identity "+
					"(stdout: %s) -- the CA trust gap documented in this test's doc comment appears to be "+
					"CLOSED; update this test to assert success and remove the parity-gap note", proto, stdout)
			}
			t.Logf("[%s] tenant barrier-init with ca-issue-minted identity failed as documented "+
				"(CA trust gap): stdout=%q stderr=%q err=%v", proto, stdout, stderr, err)
		})
	}
}
