// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// tenantAdminIdentity is a user in its own tenant, holding a role created
// there, and the client certificate it logs in with.
type tenantAdminIdentity struct {
	tenantID string
	username string
	certFile string
	keyFile  string
}

// newTenantAdminIdentity creates tenantID, a role in it carrying perms, and a
// user holding that role, then mints the user's client certificate
// (CN=username, OU=tenantID) from the node's own CA -- the CA the mTLS
// listener trusts, read from beside cfg.TLSCACert -- so the CLI can act AS
// that tenant's administrator.
//
// No caller may issue a certificate into another tenant (certs:* is
// ScopeSelfStrict), so a test that needs a tenant's certificate operations
// performs them as the tenant's own administrator, which is what this builds.
// The harness admin only provisions the tenant, role and user.
func newTenantAdminIdentity(t *testing.T, cfg *TestConfig, proto ProtocolType, tenantID string, perms ...string) *tenantAdminIdentity {
	t.Helper()
	ts := testTimestamp()
	username := fmt.Sprintf("tadmin-%s-%d", proto, ts)
	roleName := fmt.Sprintf("tadmin-role-%s-%d", proto, ts)

	run := func(what string, args ...string) string {
		t.Helper()
		stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
		if err != nil {
			t.Logf("stdout: %s", stdout)
			t.Logf("stderr: %s", stderr)
			t.Fatalf("[%s] %s failed: %v", proto, what, err)
		}
		return stdout
	}

	run("tenant create", "tenant", "create", "--id", tenantID, "--name", "Tenant "+tenantID)
	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
	})
	roleID := parseIDResponse(t, proto, "roles create", run("roles create",
		"--output", "json", "roles", "create",
		"--name", roleName, "--tenant-id", tenantID,
		"--permissions", strings.Join(perms, ",")))
	userID := parseIDResponse(t, proto, "users create", run("users create",
		"--output", "json", "users", "create", "--without-email-login-key",
		"--username", username, "--tenant-id", tenantID))
	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
		execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
	})
	run("roles assign", "roles", "assign", "--user", userID, "--role", roleID)

	caDir := filepath.Dir(cfg.TLSCACert)
	ca, err := qrdbdharness.LoadCA(cfg.TLSCACert, filepath.Join(caDir, "ca-key.pem"))
	if err != nil {
		t.Fatalf("[%s] load the node CA from %s: %v", proto, caDir, err)
	}
	kp, err := ca.Issue(t.TempDir(), qrdbdharness.WithSubject(username, tenantID))
	if err != nil {
		t.Fatalf("[%s] mint %s/%s's client certificate: %v", proto, tenantID, username, err)
	}
	return &tenantAdminIdentity{tenantID: tenantID, username: username, certFile: kp.CertPath, keyFile: kp.KeyPath}
}

// exec runs the CLI as this tenant administrator.
func (a *tenantAdminIdentity) exec(t *testing.T, cfg *TestConfig, proto ProtocolType, args ...string) (string, string, error) {
	t.Helper()
	return execCLIWithProtocolAndCert(t, cfg, proto, a.certFile, a.keyFile, args...)
}

// assertPermissionDenied requires that a CLI call failed on authorization,
// not on anything else: the CLI's error output names a permission refusal.
func assertPermissionDenied(t *testing.T, proto ProtocolType, what, stdout, stderr string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("[%s] %s succeeded (stdout: %s); want a permission refusal", proto, what, stdout)
	}
	out := strings.ToLower(stdout + stderr)
	for _, marker := range []string{"permission denied", "permissiondenied", "forbidden", "403", "not permitted"} {
		if strings.Contains(out, marker) {
			return
		}
	}
	t.Fatalf("[%s] %s failed, but not on authorization: err=%v stdout=%q stderr=%q", proto, what, err, stdout, stderr)
}
