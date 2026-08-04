// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// databaseBarrierNotConfiguredMsg is the exact error text produced by
// pkg/service.BarrierManagerNotConfiguredError, surfaced whenever
// DatabaseService.barrierMgr is nil. As of this writing pkg/server never
// calls service.WithBarrierManager / database.WithBarrierManager in the
// production daemon wiring (only pkg/service/database_service_test.go and
// pkg/database's own unit tests construct a *DatabaseBarrierManager and wire
// it in directly), so every real qrdbd deployment -- including this
// integration harness -- always has a nil per-database barrier manager. The
// CLI commands that depend on it (`database seal`, `database unseal`,
// `database barrier-status`) are therefore expected to fail with this
// message regardless of the database's `--enabled`/`--disabled` metadata
// flag set via `database update-barrier`. This is distinct from the tenant
// and system seal barriers (pkg/seal), which ARE fully wired and functional.
const databaseBarrierNotConfiguredMsg = "database barrier manager not configured"

// TestCLIMultiProtocolDatabaseBarrierEnableThenStatus creates a database,
// enables its (metadata-only) barrier flag via `database update-barrier
// --enabled`, and confirms `database barrier-status` surfaces the
// not-configured error documented above rather than a stale/incorrect
// sealed status. Also proves `database update-barrier` itself round-trips
// through --enabled and --disabled cleanly (the metadata flip that IS
// wired, exercised previously only via cli_database_test.go's
// TestCLIMultiProtocolDatabaseUpdateBarrier for the flag content -- this
// test adds barrier-status coverage on top of it).
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
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-barrier --enabled failed: %v", proto, err)
			}
			assertContains(t, stdout, "Enabled:  true",
				fmt.Sprintf("[%s] database info after update-barrier --enabled should show Enabled: true", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "barrier-status", dbName)
			if err == nil {
				t.Fatalf("[%s] expected database barrier-status to fail (barrier manager not wired), got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierNotConfiguredMsg,
				fmt.Sprintf("[%s] database barrier-status error should report the manager-not-configured reason", proto))

			t.Logf("[%s] database barrier enable-then-status passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseSeal exercises `database seal` on a database
// with the barrier metadata flag enabled. See databaseBarrierNotConfiguredMsg
// for why this currently always errors rather than sealing.
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
			if err != nil {
				t.Fatalf("[%s] database update-barrier --enabled failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "seal", dbName)
			if err == nil {
				t.Fatalf("[%s] expected database seal to fail (barrier manager not wired), got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierNotConfiguredMsg,
				fmt.Sprintf("[%s] database seal error should report the manager-not-configured reason", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseUnsealWrongSecret exercises `database unseal
// --secret` with an incorrect secret. Because the per-database barrier
// manager is never wired in production (see databaseBarrierNotConfiguredMsg),
// the request fails before any credential is even evaluated -- the error is
// identical regardless of which secret is supplied. This test pins that
// current behavior; when the barrier manager is eventually wired into
// pkg/server, this test (and TestCLIMultiProtocolDatabaseSeal /
// TestCLIMultiProtocolDatabaseBarrierEnableThenStatus) will start failing
// and must be revisited to assert the real sealed/unsealed semantics.
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
			if err != nil {
				t.Fatalf("[%s] database update-barrier --enabled failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "unseal", dbName, "--secret", "definitely-the-wrong-secret")
			if err == nil {
				t.Fatalf("[%s] expected database unseal with wrong secret to fail, got success (stdout: %s)",
					proto, stdout)
			}
			output := strings.ToLower(stdout + stderr + err.Error())
			assertContains(t, output, databaseBarrierNotConfiguredMsg,
				fmt.Sprintf("[%s] database unseal error should report the manager-not-configured reason", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseSealNonexistentDatabase verifies `database
// seal` on a database name that was never created. The barrier-manager nil
// check (pkg/service.DatabaseService.SealDatabase) runs before any lookup of
// the named database, so the error is the same manager-not-configured error
// rather than a not-found error -- this test pins that ordering.
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
			assertContains(t, output, databaseBarrierNotConfiguredMsg,
				fmt.Sprintf("[%s] database seal on nonexistent database should report the manager-not-configured reason, "+
					"not a not-found error (the nil-manager check runs first)", proto))
		})
	}
}

// TestCLIMultiProtocolDatabaseBarrierStatusWithoutEnabling verifies
// `database barrier-status` on a freshly created database whose barrier was
// never enabled via `database update-barrier --enabled`. The result is
// identical to the enabled case (databaseBarrierNotConfiguredMsg) because the
// nil barrier-manager check short-circuits before the per-database
// Enabled flag is ever consulted.
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
			assertContains(t, output, databaseBarrierNotConfiguredMsg,
				fmt.Sprintf("[%s] database barrier-status without barrier should report the manager-not-configured reason", proto))
		})
	}
}
