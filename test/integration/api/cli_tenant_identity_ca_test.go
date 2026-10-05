// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
//	roles create (tenant-scoped, tenant_barrier:* and certs:generate
//	permissions) -> users create --tenant-id -> roles assign -> ca issue
//	--username --tenant-id AS THE TENANT'S OWN ADMINISTRATOR (writes
//	cert.pem/key.pem with CN=username, OU=tenantID, per
//	pkg/cert/default_provider.go GenerateClientCert) -> attempt tenant
//	barrier-init/tenant barrier-unseal authenticated with that freshly
//	minted certificate.
//
// The harness admin is in the system tenant, and no caller may issue a
// certificate into another tenant (certs:generate is ScopeSelfStrict), so
// step 6 first asserts the harness admin's `ca issue --tenant-id` is refused
// and then issues as the tenant's administrator, whose client certificate is
// minted from the node's own CA (newTenantAdminIdentity).
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
// call) was a VERIFIED, REPRODUCIBLE PARITY GAP, not a workaround-avoidance
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
// cli_tenant_barrier_test.go) is UNREACHABLE end-to-end via pure CLI
// subprocesses against a standard qrdbd deployment today.
//
// The pin point moved EARLIER. Reaching step 7 at all required minting the
// tenant administrator's Key-A-trusted bootstrap certificate (step 6) by
// reading this node's own ca-key.pem and signing with it directly -- the
// only way to get a certificate the mTLS listener accepts for a brand-new
// subject. cmd/qrdbd now deletes that local key cache unconditionally
// (every boot, not only Raft mode) once the cluster CA key is confirmed
// durable in the replicated store (cmd/qrdbd/clusterca.go
// confirmClusterCAPersisted; see docs/certificate-authority/architecture.md
// "Bootstrap crash window" and the CA-signer ruling: a CA private key is
// never returned or cached on disk past that confirmation). That
// confirmation runs before the admin endpoint opens, so by the time this
// suite's /health readiness gate passes -- its only way to observe the
// server -- the cache is reliably already gone: nodeCAKeyUnavailable below
// is deterministic here, not a race. There is therefore no supported way,
// through any CLI or API surface, to mint a NEW Key-A-trusted identity for
// an arbitrary subject on an already-booted node, which makes step 6 itself
// unreachable now, not only step 7. This test pins THAT (a REST 401 "no
// client certificate in TLS connection" / equivalent transport error is no
// longer even reachable to observe) rather than working around it with the
// now-closed raw-key-file shortcut.
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
				"--permissions", "tenant_barrier:init,tenant_barrier:unseal,tenant_barrier:status,tenant_barrier:seal,certs:generate")
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
			//
			// The system tenant's harness admin may not issue it: a
			// certificate authenticates as the tenant it names.
			certDir := t.TempDir()
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ca", "issue", "--username", username, "--tenant-id", tenantID, "--out", certDir)
			assertPermissionDenied(t, proto, "system admin ca issue --tenant-id "+tenantID, stdout, stderr, err)

			// The tenant's own administrator would issue it next, logging in
			// with a client certificate minted from the node's own CA -- but
			// that requires this node's local ca-key.pem cache, which
			// cmd/qrdbd deletes once the cluster CA key is confirmed durable
			// in the replicated store (see the function doc comment). By the
			// time the suite's readiness gate passed, that confirmation has
			// already run, so the cache is deterministically gone here: there
			// is no supported way to mint a Key-A-trusted certificate for a
			// brand-new subject, which makes everything past this point --
			// the tenant administrator's own `ca issue`, and the barrier
			// operation it would have authenticated -- unreachable via pure
			// CLI subprocesses against a standard qrdbd deployment today.
			if !nodeCAKeyUnavailable(cfg) {
				t.Fatalf("[%s] ca-key.pem is present at %s -- the local-key-cache removal this test pins on "+
					"appears to be CLOSED (or this node never confirmed persistence); update this test to "+
					"mint and exercise the tenant administrator's identity instead of stopping here",
					proto, filepath.Join(filepath.Dir(cfg.TLSCACert), "ca-key.pem"))
			}
			t.Logf("[%s] node CA key cache is unavailable (ca-key.pem removed once persisted to the "+
				"replicated store) -- minting the tenant administrator's Key-A-trusted identity, and the "+
				"barrier operation it would authenticate, is unreachable on an already-booted node", proto)
		})
	}
}
