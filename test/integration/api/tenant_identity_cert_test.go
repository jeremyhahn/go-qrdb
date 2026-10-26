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

// tenantAdminIdentity is a user in its own tenant, holding a role created
// there, and the client certificate it logs in with.
type tenantAdminIdentity struct {
	tenantID string
	username string
	certFile string
	keyFile  string
}

// newTenantAdminIdentity creates tenantID together with a bootstrap admin --
// onboarding's mandatory first-admin credential, signed by the tenant's own
// Key B CA -- then a role in the tenant carrying perms and a user holding
// it, and mints that user's client certificate through the supported path:
// a CSR generated locally and "ca issue"d by the bootstrap admin itself.
// The bootstrap admin's built-in Admin role grants certs:issue (see
// pkg/permission/builtin_role_grants.go's registerAdminGrants), so no node
// CA key material is ever read.
//
// No caller may issue a certificate into another tenant (certs:* is
// ScopeSelfStrict), so a test that needs a tenant's certificate operations
// performs them as the tenant's own administrator, which is what this builds.
// The harness admin only provisions the tenant; the bootstrap admin creates the
// role and user, and issues the final certificate.
func newTenantAdminIdentity(t *testing.T, cfg *TestConfig, proto ProtocolType, tenantID string, perms ...string) *tenantAdminIdentity {
	t.Helper()
	ts := testTimestamp()
	username := fmt.Sprintf("tadmin-%s-%d", proto, ts)
	roleName := fmt.Sprintf("tadmin-role-%s-%d", proto, ts)

	// The role and user are created by the tenant's own bootstrap admin: the
	// built-in Admin role holds roles:create, users:create and roles:assign
	// in its own tenant, while the system tenant's admin holds no
	// cross-tenant roles:create / users:create.
	boot := newTenantBootstrapAdmin(t, cfg, proto, tenantID)

	run := func(what string, args ...string) string {
		t.Helper()
		stdout, stderr, err := boot.exec(t, cfg, proto, args...)
		if err != nil {
			t.Logf("stdout: %s", stdout)
			t.Logf("stderr: %s", stderr)
			t.Fatalf("[%s] %s failed: %v", proto, what, err)
		}
		return stdout
	}

	roleID := parseIDResponse(t, proto, "roles create", run("roles create",
		"--output", "json", "roles", "create",
		"--name", roleName, "--tenant-id", tenantID,
		"--permissions", strings.Join(perms, ",")))
	userID := parseIDResponse(t, proto, "users create", run("users create",
		"--output", "json", "users", "create", "--without-email-login-key",
		"--username", username, "--tenant-id", tenantID))
	// roles:delete is the Security Officer's alone; the tenant's delete,
	// registered by newTenantBootstrapAdmin, removes the role with it.
	t.Cleanup(func() {
		boot.exec(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
	})
	run("roles assign", "roles", "assign", "--user", userID, "--role", roleID)

	// The final identity's own CSR, issued by the bootstrap admin -- the
	// supported path: no private key ever reaches the server, and the
	// issued certificate is signed by the tenant's own Key B CA.
	identityCSRDir := t.TempDir()
	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
		"ca", "csr", "--username", username, "--out", identityCSRDir)
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] ca csr for %s failed: %v", proto, username, err)
	}
	identityCertDir := t.TempDir()
	stdout, stderr, err = boot.exec(t, cfg, proto,
		"ca", "issue", "--username", username,
		"--csr", filepath.Join(identityCSRDir, "csr.pem"),
		"--out", identityCertDir)
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] ca issue for %s (by the bootstrap admin) failed: %v", proto, username, err)
	}
	return &tenantAdminIdentity{
		tenantID: tenantID,
		username: username,
		certFile: filepath.Join(identityCertDir, "cert.pem"),
		keyFile:  filepath.Join(identityCSRDir, "key.pem"),
	}
}

// newTenantBootstrapAdmin creates tenantID together with its mandatory first
// admin -- a credential signed by the tenant's own Key B CA, never a key read
// from the node's CA material -- and returns that admin. Its built-in Admin
// role acts on its own tenant, which is how a test populates a tenant the
// harness admin may not write into. The tenant is deleted on cleanup.
func newTenantBootstrapAdmin(t *testing.T, cfg *TestConfig, proto ProtocolType, tenantID string) *tenantAdminIdentity {
	t.Helper()
	username := fmt.Sprintf("tadmin-boot-%s-%d", proto, testTimestamp())

	run := func(what string, args ...string) {
		t.Helper()
		stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
		if err != nil {
			t.Logf("stdout: %s", stdout)
			t.Logf("stderr: %s", stderr)
			t.Fatalf("[%s] %s failed: %v", proto, what, err)
		}
	}

	csrDir := t.TempDir()
	certDir := t.TempDir()
	run("ca csr (bootstrap admin)", "ca", "csr", "--username", username, "--out", csrDir)
	run("tenant create", "tenant", "create", "--id", tenantID, "--name", "Tenant "+tenantID,
		"--admin-username", username, "--admin-csr", filepath.Join(csrDir, "csr.pem"),
		"--out", certDir)
	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
	})
	return &tenantAdminIdentity{
		tenantID: tenantID,
		username: username,
		certFile: filepath.Join(certDir, "admin-cert.pem"),
		keyFile:  filepath.Join(csrDir, "key.pem"),
	}
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
