// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// tenantEntitlementsJSON mirrors the JSON shape of
// transport.TenantEntitlements as produced by `tenant get --output json`.
type tenantEntitlementsJSON struct {
	AllowedIsolationModes     []string `json:"allowed_isolation_modes"`
	AllowDedicatedRaft        bool     `json:"allow_dedicated_raft"`
	AllowVolumeStorage        bool     `json:"allow_volume_storage"`
	AllowSystemManagedBarrier bool     `json:"allow_system_managed_barrier"`
	AllowedBarrierModes       []string `json:"allowed_barrier_modes"`
}

// tenantInfoJSON mirrors the JSON shape of transport.TenantInfo as produced
// by `tenant get --output json`.
type tenantInfoJSON struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	IsolationType string                  `json:"isolation_type"`
	Entitlements  *tenantEntitlementsJSON `json:"entitlements"`
}

// getTenantJSON runs `tenant get <id> --output json` and decodes the result.
func getTenantJSON(t *testing.T, cfg *TestConfig, proto ProtocolType, tenantID string) tenantInfoJSON {
	t.Helper()
	stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "--output", "json", "tenant", "get", tenantID)
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] tenant get --output json failed: %v", proto, err)
	}
	var info tenantInfoJSON
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("[%s] failed to decode tenant get JSON: %v (stdout: %s)", proto, err, stdout)
	}
	return info
}

// assertStringSliceEqual fails the test if got and want do not contain the
// same elements in the same order.
func assertStringSliceEqual(t *testing.T, got, want []string, msg string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: length mismatch: got %v, want %v", msg, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", msg, got, want)
		}
	}
}

// TestCLIMultiProtocolTenantSetEntitlement exercises `tenant set-entitlement`
// across every flag permutation (isolation modes, dedicated-raft,
// volume-storage, system-managed-barrier, and allowed-barrier-modes) and
// verifies the effective entitlements via `tenant get --output json`. A
// second, narrower permutation proves set-entitlement fully REPLACES the
// prior entitlements (pkg/tenant/service.go UpdateEntitlements assigns
// info.Entitlements wholesale; it does not merge with the previous value).
func TestCLIMultiProtocolTenantSetEntitlement(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("ent-tenant-%s-%d", proto, ts)
			tenantName := fmt.Sprintf("Entitlement Tenant %s %d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
			})

			// Step 1: create the tenant (default prefix isolation; entitlements
			// are unset until set-entitlement is called).
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"tenant", "create", "--id", tenantID, "--name", tenantName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant create failed: %v", proto, err)
			}

			// Step 2: broad permutation -- every isolation mode, every
			// dedicated-storage feature, and every software barrier mode
			// permitted.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "set-entitlement", tenantID,
				"--isolation-modes", "prefix,volume,raft",
				"--allow-dedicated-raft",
				"--allow-volume-storage",
				"--allow-system-managed-barrier",
				"--allowed-barrier-modes", "none,passphrase,shamir",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant set-entitlement (broad) failed: %v", proto, err)
			}
			assertContains(t, stdout, tenantID,
				fmt.Sprintf("[%s] set-entitlement response should contain tenant ID", proto))

			info := getTenantJSON(t, cfg, proto, tenantID)
			if info.Entitlements == nil {
				t.Fatalf("[%s] tenant get after broad set-entitlement: entitlements is nil", proto)
			}
			assertStringSliceEqual(t, info.Entitlements.AllowedIsolationModes,
				[]string{"prefix", "volume", "raft"},
				fmt.Sprintf("[%s] broad AllowedIsolationModes", proto))
			assertTrue(t, info.Entitlements.AllowDedicatedRaft,
				fmt.Sprintf("[%s] broad AllowDedicatedRaft should be true", proto))
			assertTrue(t, info.Entitlements.AllowVolumeStorage,
				fmt.Sprintf("[%s] broad AllowVolumeStorage should be true", proto))
			assertTrue(t, info.Entitlements.AllowSystemManagedBarrier,
				fmt.Sprintf("[%s] broad AllowSystemManagedBarrier should be true", proto))
			assertStringSliceEqual(t, info.Entitlements.AllowedBarrierModes,
				[]string{"none", "passphrase", "shamir"},
				fmt.Sprintf("[%s] broad AllowedBarrierModes", proto))

			// Step 3: narrow permutation -- a single isolation mode, a single
			// barrier mode, and every dedicated-storage bool omitted (false).
			// This must fully replace the broad entitlements set above.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "set-entitlement", tenantID,
				"--isolation-modes", "prefix",
				"--allowed-barrier-modes", "passphrase",
			)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant set-entitlement (narrow) failed: %v", proto, err)
			}

			info = getTenantJSON(t, cfg, proto, tenantID)
			if info.Entitlements == nil {
				t.Fatalf("[%s] tenant get after narrow set-entitlement: entitlements is nil", proto)
			}
			assertStringSliceEqual(t, info.Entitlements.AllowedIsolationModes,
				[]string{"prefix"},
				fmt.Sprintf("[%s] narrow AllowedIsolationModes", proto))
			assertFalse(t, info.Entitlements.AllowDedicatedRaft,
				fmt.Sprintf("[%s] narrow AllowDedicatedRaft should be false (replaced, not merged)", proto))
			assertFalse(t, info.Entitlements.AllowVolumeStorage,
				fmt.Sprintf("[%s] narrow AllowVolumeStorage should be false (replaced, not merged)", proto))
			assertFalse(t, info.Entitlements.AllowSystemManagedBarrier,
				fmt.Sprintf("[%s] narrow AllowSystemManagedBarrier should be false (replaced, not merged)", proto))
			assertStringSliceEqual(t, info.Entitlements.AllowedBarrierModes,
				[]string{"passphrase"},
				fmt.Sprintf("[%s] narrow AllowedBarrierModes", proto))

			t.Logf("[%s] tenant set-entitlement permutations passed for: %s", proto, tenantID)
		})
	}
}

// TestCLIMultiProtocolTenantSetEntitlementNonexistentTenant verifies that
// `tenant set-entitlement` on a tenant ID that was never created returns a
// typed not-found error (pkg/tenant.ErrTenantNotFound, mapped to HTTP 404 by
// mapTenantError) rather than silently creating partial state.
func TestCLIMultiProtocolTenantSetEntitlementNonexistentTenant(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			tenantID := fmt.Sprintf("nonexistent-ent-tenant-%s-%d", proto, testTimestamp())

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"tenant", "set-entitlement", tenantID, "--allow-volume-storage")
			if err == nil {
				t.Fatalf("[%s] expected error setting entitlements on nonexistent tenant %q, got none (stdout: %s)",
					proto, tenantID, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, "not found",
				fmt.Sprintf("[%s] set-entitlement on nonexistent tenant should report 'not found'", proto))
		})
	}
}

// TestCLIMultiProtocolTenantBarrierInitRequiresTenantScope drives the full
// CLI-observable sequence for provisioning a per-tenant barrier: create the
// tenant, grant it every software barrier-mode entitlement via
// set-entitlement, then attempt `tenant barrier-init`.
//
// The init call is expected to FAIL: per-tenant barrier lifecycle operations
// (pkg/api/rest/handlers_tenants.go authorizeTenantBarrierOp ->
// AuthMiddleware.AuthorizeTenant, backed by permission.ScopeTenantOnly) are
// gated to an identity whose own tenant matches the target tenant in the URL;
// the system tenant is explicitly denied cross-tenant access by design. This
// CLI integration harness authenticates with the admin mTLS identity (OU=
// system tenant, see testutil.go QRDB_TLS_CERT/QRDB_TLS_KEY), so it can never
// hold tenant-scoped credentials for a tenant created ad hoc by this test.
// Exercising the success path would require minting a client certificate
// bound to the target tenant (OU=<tenantID>) plus a role/user granting
// tenant_barrier:init, which is outside the scope of the `qrdb` CLI surface
// (see test/integration/enable_encryption_test.go for the lower-level HTTP
// harness that does this). This test instead pins the documented
// authorization boundary: the entitlement grant alone is not sufficient, and
// the request is rejected before it ever reaches the entitlement check.
func TestCLIMultiProtocolTenantBarrierInitRequiresTenantScope(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("bi-tenant-%s-%d", proto, ts)
			tenantName := fmt.Sprintf("Barrier Init Tenant %s %d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"tenant", "create", "--id", tenantID, "--name", tenantName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant create failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "set-entitlement", tenantID,
				"--allowed-barrier-modes", "none,passphrase,shamir")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant set-entitlement failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "barrier-init", "--id", tenantID)
			if err == nil {
				t.Fatalf("[%s] expected tenant barrier-init to fail for the system-tenant admin identity, "+
					"got success (stdout: %s)", proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, "permission",
				fmt.Sprintf("[%s] tenant barrier-init denial should report a permission error", proto))
		})
	}
}

// TestCLIMultiProtocolTenantBarrierUnsealRequiresTenantScope mirrors
// TestCLIMultiProtocolTenantBarrierInitRequiresTenantScope for
// `tenant barrier-unseal`: the same tenant-scoped authorization gate applies
// (permission.TenantBarrierUnseal, ScopeTenantOnly), so the admin identity is
// denied regardless of the key material supplied.
func TestCLIMultiProtocolTenantBarrierUnsealRequiresTenantScope(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			tenantID := fmt.Sprintf("bu-tenant-%s-%d", proto, ts)
			tenantName := fmt.Sprintf("Barrier Unseal Tenant %s %d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "tenant", "delete", tenantID) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"tenant", "create", "--id", tenantID, "--name", tenantName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] tenant create failed: %v", proto, err)
			}

			// A syntactically valid base64 key so the CLI's own input
			// validation passes and the request actually reaches the server's
			// authorization gate.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "barrier-unseal", "--id", tenantID, "--key", "dGVzdC1rZXk=")
			if err == nil {
				t.Fatalf("[%s] expected tenant barrier-unseal to fail for the system-tenant admin identity, "+
					"got success (stdout: %s)", proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, "permission",
				fmt.Sprintf("[%s] tenant barrier-unseal denial should report a permission error", proto))
		})
	}
}

// TestCLIMultiProtocolTenantBarrierUnsealInputValidation exercises the
// client-side validation on `tenant barrier-unseal` (cmd/qrdb/tenant.go
// newTenantBarrierUnsealCmd): both failure modes are detected and reported
// before any request reaches the server, so they are protocol-independent by
// construction. The table loop still runs them once per protocol to prove
// the CLI wiring is consistent across every --server target this repo
// supports.
func TestCLIMultiProtocolTenantBarrierUnsealInputValidation(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			tenantID := fmt.Sprintf("unseal-validate-%s-%d", proto, testTimestamp())

			// No --key supplied.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"tenant", "barrier-unseal", "--id", tenantID)
			if err == nil {
				t.Fatalf("[%s] expected error when --key is not supplied, got success (stdout: %s)",
					proto, stdout)
			}
			assertContains(t, stdout+stderr, "provide --key",
				fmt.Sprintf("[%s] a missing --key should report the input error", proto))

			// Invalid base64 in --key.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "barrier-unseal", "--id", tenantID, "--key", "not-valid-base64!!")
			if err == nil {
				t.Fatalf("[%s] expected error decoding invalid base64 --key, got success (stdout: %s)", proto, stdout)
			}
			assertContains(t, stdout+stderr, "base64 decode key",
				fmt.Sprintf("[%s] invalid base64 --key should report a decode error", proto))

			// The credential unseal takes no share: a Shamir tenant barrier
			// is unsealed by its holders through barrier-quorum submit.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"tenant", "barrier-unseal", "--id", tenantID, "--share", "c2hhcmU=")
			if err == nil {
				t.Fatalf("[%s] expected --share to be refused, got success (stdout: %s)", proto, stdout)
			}
			assertContains(t, stdout+stderr, "unknown flag: --share",
				fmt.Sprintf("[%s] --share should be an unknown flag", proto))

			t.Logf("[%s] tenant barrier-unseal input validation passed", proto)
		})
	}
}
