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
	// system tenant's admin holds no cross-tenant roles:create / users:create,
	// and a tenant's first admin is the supported way to populate it. Under
	// the grant rule (go-qrdb#1278) perms must be ones the built-in Admin
	// holds.
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
	t.Cleanup(func() {
		boot.exec(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
		boot.exec(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
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

// seatProvisioner gives a second account in the admin's tenant the tenant's
// built-in Admin and Operator roles, and the Security Officer role when withSO,
// and returns that account's identity. Under the grant rule (go-qrdb#1278) a
// principal binds to itself no role carrying a permission it does not hold,
// and creates no role past its holdings, so the admin cannot take SO itself
// or write a kv:* role; assigning a built-in role to another account is
// allowed. The certificate is issued before SO is assigned: the certificate
// target guard refuses an SO target. Test setup only.
func (a *tenantAdminIdentity) seatProvisioner(t *testing.T, cfg *TestConfig, proto ProtocolType, withSO bool) *tenantAdminIdentity {
	t.Helper()

	run := func(what string, args ...string) string {
		t.Helper()
		stdout, stderr, err := a.exec(t, cfg, proto, args...)
		if err != nil {
			t.Logf("stdout: %s", stdout)
			t.Logf("stderr: %s", stderr)
			t.Fatalf("[%s] %s failed: %v", proto, what, err)
		}
		return stdout
	}

	username := fmt.Sprintf("tprov-%s-%d", proto, testTimestamp())
	userID := parseIDResponse(t, proto, "users create", run("users create",
		"--output", "json", "users", "create", "--without-email-login-key",
		"--username", username, "--tenant-id", a.tenantID))
	for _, name := range []string{"admin", "operator"} {
		roleID := parseIDResponse(t, proto, "roles get-by-name "+name,
			run("roles get-by-name "+name, "--output", "json", "roles", "get-by-name", name))
		run("roles assign ("+name+")", "roles", "assign", "--user", userID, "--role", roleID)
	}

	csrDir := t.TempDir()
	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
		"ca", "csr", "--username", username, "--out", csrDir)
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] ca csr for %s failed: %v", proto, username, err)
	}
	certDir := t.TempDir()
	run("ca issue (provisioner)", "ca", "issue", "--username", username,
		"--csr", filepath.Join(csrDir, "csr.pem"), "--out", certDir)

	if withSO {
		soRoleID := parseIDResponse(t, proto, "roles get-by-name so",
			run("roles get-by-name so", "--output", "json", "roles", "get-by-name", "so"))
		run("roles assign (security officer)", "roles", "assign", "--user", userID, "--role", soRoleID)
	}
	return &tenantAdminIdentity{
		tenantID: a.tenantID,
		username: username,
		certFile: filepath.Join(certDir, "cert.pem"),
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
