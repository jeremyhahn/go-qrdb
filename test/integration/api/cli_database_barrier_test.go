// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// databaseBarrierSharedStoreMsg is a substring of the exact error text
// produced by pkg/database.SharedStoreBarrierUnsupportedError.Error(). The
// per-database encryption barrier manager IS now wired into the production
// daemon (pkg/server/lifecycle.BuildServices wires
// database.WithBarrierManager / service.WithBarrierManager whenever the seal
// subsystem's BarrierRegistry is available -- see config.test.yaml's
// `seal: {enabled: true, strategy: software}`), so `database seal`,
// `database unseal`, and `database barrier-status` are no longer universally
// rejected with "not configured".
//
// A per-database barrier is only cryptographically real on a database with a
// DEDICATED store of its own (see pkg/database.DatabaseConfig.
// HasDedicatedStore): an explicit --data-dir, an IsolationVolume tenant's
// DataRoot, an IsolationRaft tenant's dedicated shard, or BackendRaft. This
// docker-composed standalone server has no tenant isolation or per-database
// --data-dir wired into this multi-protocol CLI harness (execCLIWithProtocol
// drives a single shared config across REST/gRPC/QUIC/MCP/Unix uniformly, and
// only REST's create-time wire path threads a --data-dir/--barrier-passphrase
// through today -- see cmd/qrdb/database.go's newDatabaseCreateCmd doc
// comment), so every database `database create` produces here shares the
// default store. `database update-barrier --enabled` is therefore expected
// to fail closed with SharedStoreBarrierUnsupportedError across every
// protocol, rather than silently flipping on inert barrier metadata.
//
// The full working lifecycle (create with a dedicated --data-dir barrier,
// barrier-status, seal, unseal with a wrong then a correct secret, and a kv
// round-trip) is covered end-to-end against a self-spawned single-node
// cluster-lane daemon in test/integration/database_barrier_cli_test.go,
// which is not constrained to this harness's uniform multi-protocol shape.
const databaseBarrierSharedStoreMsg = "encryption barrier requires a dedicated store"

// databaseBarrierTenantNotFoundMsg is the exact error text produced by
// pkg/seal.ErrTenantNotFound, surfaced by `database seal` / `database
// unseal` / `database barrier-status` for a database whose barrier was never
// successfully registered in the seal.BarrierRegistry -- which, given the
// SharedStoreBarrierUnsupportedError rejection above, is every database in
// this harness (update-barrier --enabled never gets far enough to call
// EnsureBarrier/RegisterBarrier).
const databaseBarrierTenantNotFoundMsg = "tenant not found"

// TestCLIMultiProtocolDatabaseBarrierEnableThenStatus creates a database and
// attempts to enable its barrier via `database update-barrier --enabled`,
// confirming the request fails closed with SharedStoreBarrierUnsupportedError
// (the database has no dedicated store in this harness) rather than silently
// flipping the metadata flag on, and that `database barrier-status`
// afterward reports the barrier as never having been registered.
func TestCLIMultiProtocolDatabaseBarrierEnableThenStatus(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-barrier-status-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-barrier", dbName, "--enabled")
			if err == nil {
				t.Fatalf("[%s] expected database update-barrier --enabled on a shared-store database to fail, got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierSharedStoreMsg,
				fmt.Sprintf("[%s] database update-barrier --enabled on a shared-store database should report the dedicated-store requirement", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "barrier-status", dbName)
			if err == nil {
				t.Fatalf("[%s] expected database barrier-status to fail (barrier never registered), got success (stdout: %s)",
					proto, stdout)
			}
			output = strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierTenantNotFoundMsg,
				fmt.Sprintf("[%s] database barrier-status error should report the barrier was never registered", proto))

			t.Logf("[%s] database barrier enable-then-status shared-store rejection passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseSeal exercises `database seal` on a database
// whose (metadata-only) `update-barrier --enabled` attempt was rejected. See
// databaseBarrierTenantNotFoundMsg for why this errors rather than sealing.
func TestCLIMultiProtocolDatabaseSeal(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-seal-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}

			_, _, err = execCLIWithProtocol(t, cfg, proto, "database", "update-barrier", dbName, "--enabled")
			if err == nil {
				t.Fatalf("[%s] expected database update-barrier --enabled on a shared-store database to fail", proto)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "seal", dbName)
			if err == nil {
				t.Fatalf("[%s] expected database seal to fail (barrier never registered), got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierTenantNotFoundMsg,
				fmt.Sprintf("[%s] database seal error should report the barrier was never registered", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseUnsealWrongSecret exercises `database unseal
// --secret` with an incorrect secret against a database whose barrier was
// never registered (see databaseBarrierTenantNotFoundMsg): the request fails
// before any credential is even evaluated, so the error is identical
// regardless of which secret is supplied.
func TestCLIMultiProtocolDatabaseUnsealWrongSecret(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-unseal-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}

			_, _, err = execCLIWithProtocol(t, cfg, proto, "database", "update-barrier", dbName, "--enabled")
			if err == nil {
				t.Fatalf("[%s] expected database update-barrier --enabled on a shared-store database to fail", proto)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "unseal", dbName, "--secret", "definitely-the-wrong-secret")
			if err == nil {
				t.Fatalf("[%s] expected database unseal with wrong secret to fail, got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierTenantNotFoundMsg,
				fmt.Sprintf("[%s] database unseal error should report the barrier was never registered", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseSealNonexistentDatabase verifies `database
// seal` on a database name that was never created fails with the
// tenant-not-found error (the barrier manager is configured, but nothing was
// ever registered for a name that was never even created).
func TestCLIMultiProtocolDatabaseSealNonexistentDatabase(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-never-created-%s-%d", proto, testTimestamp())

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "seal", dbName)
			if err == nil {
				t.Fatalf("[%s] expected database seal on a nonexistent database to fail, got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierTenantNotFoundMsg,
				fmt.Sprintf("[%s] database seal on nonexistent database should report the barrier was never registered", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseBarrierStatusWithoutEnabling verifies
// `database barrier-status` on a freshly created database whose barrier was
// never enabled via `database update-barrier --enabled`. The result is the
// same tenant-not-found error as the enabled-but-rejected case, since neither
// path ever reaches EnsureBarrier/RegisterBarrier for a shared-store database.
func TestCLIMultiProtocolDatabaseBarrierStatusWithoutEnabling(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-nobarrier-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "barrier-status", dbName)
			if err == nil {
				t.Fatalf("[%s] expected database barrier-status without an enabled barrier to fail, got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierTenantNotFoundMsg,
				fmt.Sprintf("[%s] database barrier-status without barrier should report the barrier was never registered", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateBarrier_DisableAlwaysAllowed verifies
// that `database update-barrier --disabled` (a no-op-safe metadata flip) is
// NOT subject to the dedicated-store restriction -- only enabling a barrier
// requires a dedicated store; disabling one is always a pure metadata write.
func TestCLIMultiProtocolDatabaseUpdateBarrier_DisableAlwaysAllowed(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-disable-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-barrier", dbName, "--disabled")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-barrier --disabled failed: %v", proto, err)
			}
			assertContains(t, stdout, "Enabled:  false",
				fmt.Sprintf("[%s] database info after update-barrier --disabled should show Enabled: false", proto))
		})
	}
}
