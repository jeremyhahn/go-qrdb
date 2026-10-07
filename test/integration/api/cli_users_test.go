// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolUsersLifecycle tests the full user lifecycle
// (create, list, get, update, disable, enable, delete) across all protocols.
func TestCLIMultiProtocolUsersLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			username := fmt.Sprintf("user-%s-%d", proto, ts)
			updatedUsername := fmt.Sprintf("user-updated-%s-%d", proto, ts)

			// Register cleanup to delete the user in case of early failure.
			var userID string
			t.Cleanup(func() {
				if userID != "" {
					execCLIWithProtocol(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
				}
			})

			// Step 1: Create a user.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "users", "create", "--without-email-login-key", "--username", username)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID:",
				fmt.Sprintf("[%s] users create should contain 'ID:'", proto))
			assertContains(t, stdout, "Username:",
				fmt.Sprintf("[%s] users create should contain 'Username:'", proto))

			// Extract user ID from output.
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					userID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if userID == "" {
				t.Fatalf("[%s] failed to extract user ID from create output", proto)
			}

			// Step 2: List users and verify the new user is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users list failed: %v", proto, err)
			}
			assertContains(t, stdout, username,
				fmt.Sprintf("[%s] users list should contain '%s'", proto, username))

			// Step 3: Get user by ID.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "get", userID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users get failed: %v", proto, err)
			}
			assertContains(t, stdout, "Username:",
				fmt.Sprintf("[%s] users get should contain 'Username:'", proto))
			assertContains(t, stdout, username,
				fmt.Sprintf("[%s] users get should contain '%s'", proto, username))

			// Step 4: Update user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "update", userID, "--username", updatedUsername)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users update failed: %v", proto, err)
			}
			assertContains(t, stdout, "Username:",
				fmt.Sprintf("[%s] users update should contain 'Username:'", proto))

			// Step 5: Disable user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "disable", userID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users disable failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] users disable should output 'OK'", proto))

			// Step 6: Enable user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "enable", userID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users enable failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] users enable should output 'OK'", proto))

			// Step 7: Delete user.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "delete", userID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users delete failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] users delete should output 'OK'", proto))

			// Clear userID so cleanup does not attempt a second delete.
			userID = ""

			t.Logf("[%s] users lifecycle passed for: %s", proto, username)
		})
	}
}

// TestCLIMultiProtocolUsersPagination tests user listing with pagination
// across all protocols.
func TestCLIMultiProtocolUsersPagination(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()

			// Create 3 users for pagination testing.
			var userIDs []string
			for i := 1; i <= 3; i++ {
				username := fmt.Sprintf("user-page-%d-%s-%d", i, proto, ts)

				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "users", "create", "--without-email-login-key", "--username", username)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] users create %s failed: %v", proto, username, err)
				}

				var uid string
				for _, line := range strings.Split(stdout, "\n") {
					if strings.HasPrefix(line, "ID:") {
						uid = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
						break
					}
				}
				if uid == "" {
					t.Fatalf("[%s] failed to extract user ID from create output for %s", proto, username)
				}
				userIDs = append(userIDs, uid)
			}

			// Register cleanup to delete all created users.
			t.Cleanup(func() {
				for _, uid := range userIDs {
					execCLIWithProtocol(t, cfg, proto, "users", "delete", uid) //nolint:errcheck
				}
			})

			// List users with pagination.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "users", "list", "--page", "1", "--page-size", "2")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users list --page 1 --page-size 2 failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout,
				fmt.Sprintf("[%s] users list with pagination should not be empty", proto))

			// Delete all created users.
			for _, uid := range userIDs {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "delete", uid)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] users delete %s failed: %v", proto, uid, err)
				}
			}

			// Clear userIDs so cleanup does not attempt redundant deletes.
			userIDs = nil

			t.Logf("[%s] users pagination passed", proto)
		})
	}
}

// TestCLIMultiProtocolUsersCreateFlags tests additional create flags
// (--tenant-id, --roles) across all protocols. The --tenant-id user is created
// by the target tenant's own administrator: the system tenant's admin may not
// write into another tenant.
func TestCLIMultiProtocolUsersCreateFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()

			// Sub-test 1: Create a user with --tenant-id, as that tenant's own
			// administrator.
			tenantID := fmt.Sprintf("user-tenant-%s-%d", proto, ts)
			admin := newTenantBootstrapAdmin(t, cfg, proto, tenantID)
			tenantUsername := fmt.Sprintf("user-tenant-%s-%d", proto, ts)
			var tenantUserID string
			t.Cleanup(func() {
				if tenantUserID != "" {
					admin.exec(t, cfg, proto, "users", "delete", tenantUserID) //nolint:errcheck
				}
			})

			stdout, stderr, err := admin.exec(t, cfg, proto,
				"users", "create", "--without-email-login-key", "--username", tenantUsername, "--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create --tenant-id failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID:",
				fmt.Sprintf("[%s] users create --tenant-id should contain 'ID:'", proto))
			assertContains(t, stdout, tenantID,
				fmt.Sprintf("[%s] users create --tenant-id should contain tenant in output", proto))

			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					tenantUserID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if tenantUserID == "" {
				t.Fatalf("[%s] failed to extract user ID from create output", proto)
			}

			// Sub-test 2: Create a role, then create a user with --roles.
			roleName := fmt.Sprintf("userrole-%s-%d", proto, ts)
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"roles", "create", "--name", roleName, "--permissions", "kv:get")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles create for --roles flag test failed: %v", proto, err)
			}

			var roleID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					roleID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if roleID == "" {
				t.Fatalf("[%s] failed to extract role ID from create output", proto)
			}

			roleUsername := fmt.Sprintf("user-roles-%s-%d", proto, ts)
			var roleUserID string
			t.Cleanup(func() {
				if roleUserID != "" {
					execCLIWithProtocol(t, cfg, proto, "users", "delete", roleUserID) //nolint:errcheck
				}
				if roleID != "" {
					execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID) //nolint:errcheck
				}
			})

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"users", "create", "--without-email-login-key", "--username", roleUsername, "--roles", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create --roles failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID:",
				fmt.Sprintf("[%s] users create --roles should contain 'ID:'", proto))

			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					roleUserID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if roleUserID == "" {
				t.Fatalf("[%s] failed to extract user ID from create --roles output", proto)
			}

			// Verify user get shows the role.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "get", roleUserID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users get after --roles create failed: %v", proto, err)
			}
			assertContains(t, stdout, roleID,
				fmt.Sprintf("[%s] users get should contain role ID '%s'", proto, roleID))

			// Cleanup: delete users and role.
			stdout, stderr, err = admin.exec(t, cfg, proto, "users", "delete", tenantUserID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users delete tenant user failed: %v", proto, err)
			}
			tenantUserID = ""

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "users", "delete", roleUserID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users delete role user failed: %v", proto, err)
			}
			roleUserID = ""

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "roles", "delete", roleID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] roles delete failed: %v", proto, err)
			}
			roleID = ""

			t.Logf("[%s] users create flags passed", proto)
		})
	}
}

// TestCLIMultiProtocolUsersListTenantFilter tests user listing with
// --tenant-id filtering across all protocols, as the tenant's own administrator.
func TestCLIMultiProtocolUsersListTenantFilter(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			username := fmt.Sprintf("user-tfilter-%s-%d", proto, ts)
			tenantID := fmt.Sprintf("user-tfilter-%s-%d", proto, ts)
			admin := newTenantBootstrapAdmin(t, cfg, proto, tenantID)

			// Create a user with --tenant-id, as that tenant's own administrator.
			var userID string
			t.Cleanup(func() {
				if userID != "" {
					admin.exec(t, cfg, proto, "users", "delete", userID) //nolint:errcheck
				}
			})

			stdout, stderr, err := admin.exec(t, cfg, proto,
				"users", "create", "--without-email-login-key", "--username", username, "--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users create --tenant-id failed: %v", proto, err)
			}

			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					userID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if userID == "" {
				t.Fatalf("[%s] failed to extract user ID from create output", proto)
			}

			// List users with --tenant-id filter.
			stdout, stderr, err = admin.exec(t, cfg, proto,
				"users", "list", "--tenant-id", tenantID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users list --tenant-id failed: %v", proto, err)
			}
			assertContains(t, stdout, username,
				fmt.Sprintf("[%s] users list --tenant-id should contain '%s'", proto, username))

			// Cleanup: delete the user.
			stdout, stderr, err = admin.exec(t, cfg, proto, "users", "delete", userID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] users delete failed: %v", proto, err)
			}
			userID = ""

			t.Logf("[%s] users list tenant filter passed", proto)
		})
	}
}
