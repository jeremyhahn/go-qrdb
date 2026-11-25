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

// TestCLIMultiProtocolGroupsLifecycle drives the IAM group lifecycle
// (go-qrdb#1651) -- create, get, get-by-name, update, list, delete -- through
// `qrdb groups` on every protocol.
func TestCLIMultiProtocolGroupsLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}
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

			ts := testTimestamp()
			name := fmt.Sprintf("grp-%s-%d", proto, ts)
			groupID := parseIDResponse(t, proto, "groups create", run("groups create",
				"--output", "json", "groups", "create", "--name", name, "--description", "first"))
			deleted := false
			t.Cleanup(func() {
				if !deleted {
					execCLIWithProtocol(t, cfg, proto, "groups", "delete", groupID) //nolint:errcheck
				}
			})

			// A second group of the same name in the tenant is refused.
			if stdout, _, err := execCLIWithProtocol(t, cfg, proto, "groups", "create", "--name", name); err == nil {
				t.Fatalf("[%s] duplicate groups create succeeded: %s", proto, stdout)
			}

			out := run("groups get", "groups", "get", groupID)
			assertContains(t, out, "Name:        "+name, fmt.Sprintf("[%s] groups get should show the name", proto))
			assertContains(t, out, "Description: first", fmt.Sprintf("[%s] groups get should show the description", proto))

			out = run("groups get-by-name", "--output", "json", "groups", "get-by-name", name)
			assertContains(t, out, groupID, fmt.Sprintf("[%s] groups get-by-name should return the group", proto))

			renamed := name + "-r"
			out = run("groups update", "groups", "update", groupID, "--name", renamed, "--description", "second")
			assertContains(t, out, "Name:        "+renamed, fmt.Sprintf("[%s] groups update should rename", proto))
			assertContains(t, out, "Description: second", fmt.Sprintf("[%s] groups update should set the description", proto))

			out = run("groups list", "--output", "json", "groups", "list", "--page-size", "1000")
			assertContains(t, out, groupID, fmt.Sprintf("[%s] groups list should include the group", proto))

			out = run("groups delete", "groups", "delete", groupID)
			assertContains(t, out, "OK", fmt.Sprintf("[%s] groups delete should output 'OK'", proto))
			deleted = true

			if stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "groups", "get", groupID); err == nil {
				t.Fatalf("[%s] groups get after delete succeeded: %s", proto, stdout)
			} else {
				assertContains(t, stderr, "not found",
					fmt.Sprintf("[%s] groups get after delete should be not found, got: %s", proto, stderr))
			}
		})
	}
}

// TestCLIMultiProtocolGroupsMembershipGrantsRoles proves on every protocol
// that a group's roles reach its members (go-qrdb#1651): a user holding no
// role of its own gains the group role's permission while it is a member,
// as its own `roles own-permissions` answers, and loses it on removal. The
// tenant Admin manages the group; deleting it is the Security Officer's
// alone, as roles:delete is.
func TestCLIMultiProtocolGroupsMembershipGrantsRoles(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("grp-tenant-%s-%d", proto, ts)
			// The kv:get role needs a creator holding kv:get (the grant
			// rule), so a second account holding Admin and Operator writes
			// the role and the group. The tenant's delete, registered by
			// newTenantBootstrapAdmin, removes everything created here.
			admin := newTenantBootstrapAdmin(t, cfg, proto, tenantID).seatProvisioner(t, cfg, proto, false)
			run := func(what string, args ...string) string {
				t.Helper()
				stdout, stderr, err := admin.exec(t, cfg, proto, args...)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] %s failed: %v", proto, what, err)
				}
				return stdout
			}

			roleID := parseIDResponse(t, proto, "roles create", run("roles create",
				"--output", "json", "roles", "create", "--name", fmt.Sprintf("grp-role-%d", ts),
				"--tenant-id", tenantID, "--permissions", "kv:get"))

			// The member: an account with no role of its own and its own
			// certificate, issued by the tenant admin from the member's CSR.
			username := fmt.Sprintf("grp-member-%s-%d", proto, ts)
			userID := parseIDResponse(t, proto, "users create", run("users create",
				"--output", "json", "users", "create", "--without-email-login-key",
				"--username", username, "--tenant-id", tenantID))
			csrDir := t.TempDir()
			if stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ca", "csr", "--username", username, "--out", csrDir); err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ca csr for %s failed: %v", proto, username, err)
			}
			certDir := t.TempDir()
			run("ca issue (member)", "ca", "issue", "--username", username,
				"--csr", filepath.Join(csrDir, "csr.pem"), "--out", certDir)
			ownPermissions := func() string {
				t.Helper()
				stdout, stderr, err := execCLIWithProtocolAndCert(t, cfg, proto,
					filepath.Join(certDir, "cert.pem"), filepath.Join(csrDir, "key.pem"),
					"--output", "json", "roles", "own-permissions")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] member roles own-permissions failed: %v", proto, err)
				}
				return stdout
			}
			if out := ownPermissions(); strings.Contains(out, `"kv:get"`) {
				t.Fatalf("[%s] member holds kv:get before joining the group: %s", proto, out)
			}

			groupID := parseIDResponse(t, proto, "groups create", run("groups create",
				"--output", "json", "groups", "create", "--name", fmt.Sprintf("grp-%d", ts),
				"--tenant-id", tenantID, "--roles", roleID))

			out := run("groups add-member", "groups", "add-member", "--user", userID, "--group", groupID)
			assertContains(t, out, "OK", fmt.Sprintf("[%s] groups add-member should output 'OK'", proto))
			out = run("groups members", "groups", "members", groupID)
			assertContains(t, out, username, fmt.Sprintf("[%s] groups members should list the member", proto))

			if out := ownPermissions(); !strings.Contains(out, `"kv:get"`) {
				t.Fatalf("[%s] member lacks the group role's kv:get: %s", proto, out)
			}

			out = run("groups remove-member", "groups", "remove-member", "--user", userID, "--group", groupID)
			assertContains(t, out, "OK", fmt.Sprintf("[%s] groups remove-member should output 'OK'", proto))
			if out := ownPermissions(); strings.Contains(out, `"kv:get"`) {
				t.Fatalf("[%s] member still holds kv:get after removal: %s", proto, out)
			}

			stdout, stderr, err := admin.exec(t, cfg, proto, "groups", "delete", groupID)
			assertPermissionDenied(t, proto, "groups delete (tenant Admin)", stdout, stderr, err)
		})
	}
}
