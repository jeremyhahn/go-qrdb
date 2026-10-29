// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolRolesLifecycle tests the full role lifecycle
// (create, list, get, update, delete) across all protocols.
func TestCLIMultiProtocolRolesLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			roleName := fmt.Sprintf("role-%s-%d", proto, ts)
			updatedRoleName := fmt.Sprintf("role-updated-%s-%d", proto, ts)

			// Step 1: Create a role.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"roles", "create",
				"--name", roleName,
				"--permissions", "kv:get,kv:put")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles create failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID:",
				fmt.Sprintf("[%s] roles create should contain 'ID:'", proto))
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] roles create should contain 'Name:'", proto))

			// Extract role ID.
			var roleID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					roleID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if roleID == "" {
				t.Fatalf("[%s] roles create: could not extract role ID from output", proto)
			}

			// Register cleanup to delete the role in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
			})

			// Step 2: List roles and verify the new role is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles list failed: %v", proto, err)
			}
			assertContains(t, stdout, roleName,
				fmt.Sprintf("[%s] roles list should contain '%s'", proto, roleName))

			// Step 3: Get role by ID.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "get", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles get failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] roles get should contain 'Name:'", proto))
			assertContains(t, stdout, roleName,
				fmt.Sprintf("[%s] roles get should contain '%s'", proto, roleName))

			// Step 4: Update the role.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"roles", "update", roleID,
				"--name", updatedRoleName,
				"--permissions", "kv:get,kv:put,admin:status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles update failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] roles update should contain 'Name:'", proto))

			// Step 5: Delete the role.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] roles delete should output 'OK'", proto))

			t.Logf("[%s] roles lifecycle passed for: %s", proto, roleName)
		})
	}
}

// TestCLIMultiProtocolRolesAssignment tests role assignment and unassignment
// to users across all protocols.
func TestCLIMultiProtocolRolesAssignment(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()

			// Step 1: Create a user.
			username := fmt.Sprintf("roletest-%s-%d", proto, ts)
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"users", "create", "--without-email-login-key", "--username", username)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create failed: %v", proto, err)
			}

			var userID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					userID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if userID == "" {
				t.Fatalf("[%s] users create: could not extract user ID from output", proto)
			}

			// Step 2: Create a role.
			roleName := fmt.Sprintf("assign-role-%s-%d", proto, ts)
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"roles", "create",
				"--name", roleName,
				"--permissions", "kv:get")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles create failed: %v", proto, err)
			}

			var roleID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					roleID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if roleID == "" {
				t.Fatalf("[%s] roles create: could not extract role ID from output", proto)
			}

			// Register cleanup to delete both role and user in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
			})

			// Step 3: Assign role to user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"roles", "assign",
				"--user", userID,
				"--role", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles assign failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] roles assign should output 'OK'", proto))

			// Step 4: Unassign role from user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"roles", "unassign",
				"--user", userID,
				"--role", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles unassign failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] roles unassign should output 'OK'", proto))

			// Step 5: Cleanup - delete role and user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] roles delete should output 'OK'", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "delete", userID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] users delete should output 'OK'", proto))

			t.Logf("[%s] roles assignment passed for user: %s, role: %s", proto, username, roleName)
		})
	}
}

// TestCLIMultiProtocolRolesPermissions tests listing available permissions
// across all protocols.
func TestCLIMultiProtocolRolesPermissions(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "roles", "permissions")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles permissions failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] roles permissions should produce output", proto))

			t.Logf("[%s] roles permissions passed", proto)
		})
	}
}

// TestCLIMultiProtocolRolesCreateTenant tests the --tenant-id flag on
// role creation across all protocols.
func TestCLIMultiProtocolRolesCreateTenant(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			roleName := fmt.Sprintf("role-tenant-%s-%d", proto, ts)
			tenantID := fmt.Sprintf("role-tenant-%s-%d", proto, ts)
			// The kv:get role needs a creator holding kv:get, so a second
			// account holding Admin and Operator writes it (the grant rule);
			// it holds SO too, for the roles delete this test asserts.
			admin := newTenantBootstrapAdmin(t, cfg, proto, tenantID).seatProvisioner(t, cfg, proto, true)

			var roleID string
			t.Cleanup(func() {
				if roleID != "" {
					admin.exec(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
				}
			})

			// Create a role with --tenant-id.
			stdout, stderr, err := admin.exec(t, cfg, proto,
				"roles", "create",
				"--name", roleName,
				"--permissions", "kv:get",
				"--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles create --tenant-id failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID:",
				fmt.Sprintf("[%s] roles create --tenant-id should contain 'ID:'", proto))
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] roles create --tenant-id should contain 'Name:'", proto))

			// Extract role ID.
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					roleID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if roleID == "" {
				t.Fatalf("[%s] roles create --tenant-id: could not extract role ID", proto)
			}

			// Cleanup: delete the role.
			stdout, stderr, err = admin.exec(t, cfg, proto, "roles", "delete", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] roles delete should output 'OK'", proto))
			roleID = ""

			t.Logf("[%s] roles create tenant passed for: %s", proto, roleName)
		})
	}
}

// TestCLIMultiProtocolRolesListPagination tests pagination and tenant
// filtering on roles list across all protocols.
func TestCLIMultiProtocolRolesListPagination(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("role-page-%s-%d", proto, ts)
			// The kv:get roles need a creator holding kv:get, so a second
			// account holding Admin and Operator writes them (the grant rule).
			admin := newTenantBootstrapAdmin(t, cfg, proto, tenantID).seatProvisioner(t, cfg, proto, false)

			// Create 3 roles for pagination testing.
			var roleIDs []string
			for i := 1; i <= 3; i++ {
				roleName := fmt.Sprintf("role-page-%d-%s-%d", i, proto, ts)

				stdout, stderr, err := admin.exec(t, cfg, proto,
					"roles", "create",
					"--name", roleName,
					"--permissions", "kv:get",
					"--tenant-id", tenantID)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] roles create %s failed: %v", proto, roleName, err)
				}

				var rid string
				for _, line := range strings.Split(stdout, "\n") {
					if strings.HasPrefix(line, "ID:") {
						rid = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
						break
					}
				}
				if rid == "" {
					t.Fatalf("[%s] failed to extract role ID from create output for %s", proto, roleName)
				}
				roleIDs = append(roleIDs, rid)
			}

			// Register cleanup to delete all created roles.
			t.Cleanup(func() {
				for _, rid := range roleIDs {
					admin.exec(t, cfg, proto, "roles", "delete", rid) //nolint:errcheck
				}
			})

			// List roles with pagination.
			stdout, stderr, err := admin.exec(t, cfg, proto,
				"roles", "list", "--page", "1", "--page-size", "2")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles list --page 1 --page-size 2 failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] roles list with pagination should not be empty", proto))

			// List roles with --tenant-id filter.
			stdout, stderr, err = admin.exec(t, cfg, proto,
				"roles", "list", "--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles list --tenant-id failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] roles list --tenant-id should not be empty", proto))

			// Delete all created roles.
			for _, rid := range roleIDs {
				stdout, stderr, err = admin.exec(t, cfg, proto, "roles", "delete", rid)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] roles delete %s failed: %v", proto, rid, err)
				}
			}

			// Clear roleIDs so cleanup does not attempt redundant deletes.
			roleIDs = nil

			t.Logf("[%s] roles list pagination passed", proto)
		})
	}
}
