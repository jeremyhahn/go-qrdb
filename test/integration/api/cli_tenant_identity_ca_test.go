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
	"sync"
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

// defaultVolumePoolOnce guards registerDefaultVolumePoolOnce so the shared
// docker-compose qrdbd server's "default" pool catalog record is created at
// most once per test binary run, regardless of how many tests need it.
var defaultVolumePoolOnce sync.Once

// registerDefaultVolumePoolOnce registers the "default" volume pool in the
// replicated catalog via `qrdb volume-pool create`, mirroring
// test/integration/helpers_test.go's createVolumePoolREST. docs/volume-pools/
// architecture.md decision D2: a pool's catalog record always comes from an
// explicit call, never implicitly from a node's storage.volume_pools config,
// so any fixture relying on self-service tenant registration against a
// dedicated per-tenant barrier must call this (or an equivalent) first.
func registerDefaultVolumePoolOnce(t *testing.T, cfg *TestConfig, proto ProtocolType) {
	t.Helper()
	defaultVolumePoolOnce.Do(func() {
		stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "volume-pool", "create", "default")
		if err != nil && !strings.Contains(stdout+stderr, "already exists") {
			t.Fatalf("volume-pool create default: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
		}
	})
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
// ceremony for minting a tenant's first-admin mTLS identity and using it:
//
//	ca csr (local keygen for the admin-to-be) -> tenant create
//	--admin-username --admin-csr --out (the tenant and its first admin are
//	created as ONE atomic unit, pkg/service/tenant_onboarding.go
//	OnboardTenant; the admin's certificate is signed by the TENANT'S OWN
//	Key B CA and written to --out as admin-cert.pem; the admin's account
//	already carries the tenant's built-in Admin role, which grants
//	tenant_barrier:init/unseal/seal/status -- see
//	pkg/permission/builtin_role_grants.go) -> tenant set-entitlement (allow
//	barrier modes) -> tenant barrier-init authenticated with that admin
//	certificate.
//
// This replaces the old flow's separate roles-create/users-create/
// roles-assign/ca-issue-as-a-different-identity sequence: server-side keygen
// is retired (no private key ever leaves the CLI, not even the admin's), and
// "ca issue --tenant-id" no longer exists -- a tenant's first admin identity
// is minted exactly once, by tenant creation itself.
//
// Step "the system tenant's harness admin may not issue a cert for this
// account" from the old flow is preserved as a distinct assertion: since
// certs:issue is ScopeSelfStrict and confined to the CALLER's own tenant
// (never a --tenant-id argument, which no longer exists on `ca issue`), the
// harness admin's own attempt to `ca issue --username <the tenant admin's
// username>` necessarily targets the SYSTEM tenant's account namespace,
// where that username does not exist, and is refused as a CSR for a
// nonexistent account -- not merely a permission check on a flag that no
// longer exists.
//
// The parity gap this test originally pinned (a `ca issue`-minted
// certificate never trusted by this node's own mTLS listener) remains
// closed: cmd/qrdbd/tls.go's TLSBundle installs a DYNAMIC client-auth trust
// pool (pkg/clienttrust), reading every tenant's Key B CA certificate from
// the node's own cert.CertProvider in addition to the cluster CA (Key A), on
// every connection, so the admin certificate minted by tenant creation here
// authenticates on this node's own mTLS listener.
func TestCLIMultiProtocolTenantIdentityBarrierFlow(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	// A volume pool is required for ANY dedicated per-tenant barrier
	// (config.test.yaml's storage.volume_pools doc comment): without a
	// catalog record for "default", barrier-init below fails closed with
	// "volume pool not configured on barrier registry". storage.volume_pools
	// only maps the pool for THIS node to serve; the catalog record itself
	// comes only from an explicit create call (docs/volume-pools/architecture.md
	// D2), so register it here the way createVolumePoolREST does in
	// test/integration/helpers_test.go. Tolerate a prior run already having
	// created it.
	registerDefaultVolumePoolOnce(t, cfg, identityTLSProtocols()[0])

	for _, proto := range identityTLSProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("id-tenant-%s-%d", proto, ts)
			tenantName := fmt.Sprintf("Identity Tenant %s %d", proto, ts)
			username := fmt.Sprintf("tenantadmin-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
			})

			// Step 1: ca csr -- generate the admin-to-be's key and CSR
			// locally; no account or tenant needs to exist yet.
			csrDir := t.TempDir()
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ca", "csr", "--username", username, "--out", csrDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca csr failed: %v", proto, err)
			}
			csrFile := filepath.Join(csrDir, "csr.pem")
			keyFile := filepath.Join(csrDir, "key.pem")

			// Step 2: tenant create --admin-username --admin-csr --out. The
			// tenant and its first admin are created together; the admin's
			// certificate is signed by the tenant's own Key B CA and written
			// to admin-cert.pem. The tenant is isolated behind its own
			// barrier, so the admin waits as the pending first admin whose
			// SPKI is pinned as the barrier's epoch-1 anchor -- the anchor
			// the barrier-init statement in step 5 is judged against.
			certDir := t.TempDir()
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "create", "--id", tenantID, "--name", tenantName,
				"--isolation-type", "barrier",
				"--admin-username", username, "--admin-csr", csrFile, "--out", certDir)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant create failed: %v", proto, err)
			}
			certFile := filepath.Join(certDir, "admin-cert.pem")
			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				t.Fatalf("[%s] tenant create did not write %s: %v", proto, certFile, err)
			}

			// Verify the admin certificate carries the expected identity
			// shape: CN=username, OU=[tenantID].
			block, _ := pem.Decode(certPEM)
			if block == nil {
				t.Fatalf("[%s] admin-cert.pem is not valid PEM", proto)
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("[%s] failed to parse the admin certificate: %v", proto, err)
			}
			assertEqual(t, username, cert.Subject.CommonName,
				fmt.Sprintf("[%s] admin cert CommonName should equal the requested username", proto))
			if len(cert.Subject.OrganizationalUnit) != 1 || cert.Subject.OrganizationalUnit[0] != tenantID {
				t.Fatalf("[%s] admin cert OU = %v, want [%s]",
					proto, cert.Subject.OrganizationalUnit, tenantID)
			}

			// Step 3: set-entitlement -- allow the software barrier modes so
			// barrier-init below is not blocked by entitlements.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "set-entitlement", tenantID,
				"--allowed-barrier-modes", "none,passphrase,shamir")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant set-entitlement failed: %v", proto, err)
			}

			// Step 4: the harness admin, in the system tenant, cannot issue a
			// certificate for this username: certs:issue is confined to the
			// CALLER's own tenant, which has no account by this name -- a
			// CSR for a nonexistent account is refused.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", username, "--csr", csrFile)
			if err == nil {
				t.Fatalf("[%s] system admin ca issue for %s should fail (no such account in the system tenant); stdout: %s", proto, username, stdout)
			}
			// Specifically: refused because the account does not exist in
			// the CALLER's own tenant (*transport.UserNotFoundError) --
			// not some unrelated failure (a connectivity drop, a
			// permission refusal) that would also make err non-nil for the
			// wrong reason.
			assertContains(t, stderr, "not found",
				fmt.Sprintf("[%s] ca issue for a nonexistent account should be refused as not found, got: %s", proto, stderr))

			// Step 5 (the former PARITY GAP -- see the function doc comment,
			// now closed): use the admin certificate tenant create minted,
			// signed by the tenant's OWN Key B CA, to authenticate a
			// tenant-scoped barrier operation. The admin's built-in Admin
			// role already grants tenant_barrier:init.
			stdout, stderr, err = execCLIWithProtocolAndCert(t, cfg, proto, certFile, keyFile,
				"tenant", "barrier-init", "--id", tenantID,
				"--strategy", "passphrase", "--passphrase", "identity-flow-test-passphrase")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant barrier-init with the tenant-create-minted admin identity failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] tenant barrier-init with the tenant-create-minted admin identity should output 'OK'", proto))
		})
	}
}
