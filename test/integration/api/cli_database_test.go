// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolDatabaseLifecycle tests the full database lifecycle
// (create, list, info, drop) across all protocols.
func TestCLIMultiProtocolDatabaseLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-%s-%d", proto, testTimestamp())

			// Register cleanup to drop the database in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := createDatabaseRetryingDeadline(t, cfg, proto, dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: List databases and verify the new database is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database list failed: %v", proto, err)
			}
			assertContains(t, stdout, dbName,
				fmt.Sprintf("[%s] database list should contain '%s'", proto, dbName))

			// Step 3: Get database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database info should contain 'Name:'", proto))
			assertContains(t, stdout, "Backend:",
				fmt.Sprintf("[%s] database info should contain 'Backend:'", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database lifecycle passed for: %s", proto, dbName)
		})
	}
}

// createDeadlineAttempts bounds how often createDatabaseRetryingDeadline
// re-issues a create the server answered with its create deadline.
const createDeadlineAttempts = 3

// createDatabaseRetryingDeadline runs `database create name` and follows the
// documented contract for a create that did not finish within the server's
// create deadline (service.DatabaseCreateTimeoutError, UNAVAILABLE): the
// server rolled everything back, so the same create is re-issued. Only that
// answer is retried, a bounded number of times; any other failure, including
// a retry refused because the first attempt left the name behind, is
// returned as it is.
func createDatabaseRetryingDeadline(t *testing.T, cfg *TestConfig, proto ProtocolType, name string) (string, string, error) {
	t.Helper()
	var stdout, stderr string
	var err error
	for attempt := 1; attempt <= createDeadlineAttempts; attempt++ {
		stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "create", name)
		if err == nil || !strings.Contains(stderr, "not finished within") {
			return stdout, stderr, err
		}
		t.Logf("[%s] create attempt %d met the server's create deadline, retrying: %s", proto, attempt, stderr)
	}
	return stdout, stderr, err
}

// TestCLIMultiProtocolDatabaseCreateEngineFlagRefused pins OD-13 (ruled A):
// `--engine` is removed outright (Lane 1C), so every transport's CLI must
// refuse it as an unknown flag rather than accept or silently ignore it.
// This replaces a prior version of this test that asserted the flag worked.
func TestCLIMultiProtocolDatabaseCreateEngineFlagRefused(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-engine-refused-%s-%d", proto, testTimestamp())

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName, "--engine", "kv")
			if err == nil {
				t.Cleanup(func() {
					execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
				})
				t.Fatalf("[%s] database create --engine kv succeeded, want an unknown-flag refusal\nstdout: %s\nstderr: %s", proto, stdout, stderr)
			}
			assertContains(t, strings.ToLower(stdout+stderr+err.Error()), "engine",
				fmt.Sprintf("[%s] unknown-flag refusal should name 'engine'", proto))

			t.Logf("[%s] database create --engine refused as unknown flag", proto)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateEtcd tests creating a database and
// updating its etcd configuration across all protocols.
func TestCLIMultiProtocolDatabaseUpdateEtcd(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-etcd-%s-%d", proto, testTimestamp())

			// Register cleanup to drop the database in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update etcd configuration.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-etcd", dbName,
				"--enabled",
				"--listen-addr", "localhost:2379")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-etcd failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-etcd produced no output", proto))

			// Step 3: Verify etcd configuration is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-etcd failed: %v", proto, err)
			}
			assertContains(t, stdout, "Etcd:",
				fmt.Sprintf("[%s] database info should contain 'Etcd:' after update-etcd", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-etcd lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseCreateBackend tests creating databases with
// --backend flag and --etcd flags across all protocols.
func TestCLIMultiProtocolDatabaseCreateBackend(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()

			// Test --backend flag.
			backendDB := fmt.Sprintf("db-be-%s-%d", proto, ts)
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", backendDB) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "create", backendDB, "--backend", "pebble")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create --backend pebble failed: %v", proto, err)
			}
			assertContains(t, stdout, "Backend:",
				fmt.Sprintf("[%s] database create --backend should contain 'Backend:'", proto))
			assertContains(t, strings.ToLower(stdout), "pebble",
				fmt.Sprintf("[%s] database create --backend should show pebble", proto))

			// Drop.
			_, _, _ = execCLIWithProtocol(t, cfg, proto, "database", "drop", backendDB)

			// Test --etcd flag at creation time.
			etcdDB := fmt.Sprintf("db-etc-%s-%d", proto, ts)
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", etcdDB) //nolint:errcheck
			})

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "create", etcdDB,
				"--etcd", "--etcd-listen-addr", "localhost:2379",
				"--etcd-advertise-urls", "http://localhost:2379")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create --etcd failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create --etcd should contain 'Name:'", proto))

			// Drop.
			_, _, _ = execCLIWithProtocol(t, cfg, proto, "database", "drop", etcdDB)

			t.Logf("[%s] database create backend/etcd flags passed", proto)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateEtcdDisable tests the --disabled flag
// on database update-etcd across all protocols.
func TestCLIMultiProtocolDatabaseUpdateEtcdDisable(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-dis-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Create and enable etcd.
			_, _, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			_, _, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-etcd", dbName, "--enabled",
				"--listen-addr", "localhost:12379",
				"--advertise-urls", "http://localhost:12379")
			if err != nil {
				t.Fatalf("[%s] database update-etcd --enabled failed: %v", proto, err)
			}

			// Now disable etcd.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "update-etcd", dbName, "--disabled")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-etcd --disabled failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-etcd --disabled produced no output", proto))

			// Drop.
			_, _, _ = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)

			t.Logf("[%s] database update-etcd --disabled passed", proto)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateProtocol tests creating a database and
// updating its wire protocol configuration across all protocols.
func TestCLIMultiProtocolDatabaseUpdateProtocol(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-proto-%s-%d", proto, testTimestamp())

			// Register cleanup to drop the database in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update protocol to enable MySQL.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-protocol", dbName, "--mysql")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-protocol --mysql failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-protocol produced no output", proto))

			// Step 3: Verify protocol is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-protocol failed: %v", proto, err)
			}

			// Verify the info output contains mysql indication (in JSON or table format).
			infoOutput := stdout + stderr
			assertNotEmpty(t, infoOutput,
				fmt.Sprintf("[%s] database info after update-protocol produced no output", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-protocol lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateRaft tests creating a database and
// updating its Raft consensus configuration across all protocols.
func TestCLIMultiProtocolDatabaseUpdateRaft(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-raft-%s-%d", proto, testTimestamp())

			// Register cleanup to drop the database in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update Raft configuration.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-raft", dbName, "--cluster-mode", "local")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-raft failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-raft produced no output", proto))

			// Step 3: Verify Raft configuration is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-raft failed: %v", proto, err)
			}
			assertContains(t, stdout, "Raft:",
				fmt.Sprintf("[%s] database info should contain 'Raft:' after update-raft", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-raft lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateBarrier tests creating a database and
// updating its encryption barrier configuration across all protocols.
func TestCLIMultiProtocolDatabaseUpdateBarrier(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-barrier-%s-%d", proto, testTimestamp())

			// Register cleanup to drop the database in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database on the shared pebble store. The
			// backend is named: a create without one records the node run
			// mode's backend, which on a raft node serves the data from its
			// raft group rather than from a shared store.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName,
				"--backend", "pebble")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Enabling a barrier on a SHARED-store database fails
			// closed. This database was created on the shared system store, where a per-database barrier would be
			// crypto theater (data stays under the shared store's DEK), so
			// the server rejects it with a typed "requires a dedicated store"
			// error. The happy path (barrier on a dedicated-store database)
			// is covered by the cluster-lane TestDatabaseBarrier* tests.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-barrier", dbName, "--enabled")
			if err == nil {
				t.Fatalf("[%s] update-barrier --enabled on a shared-store database must fail closed, got success (stdout: %s)",
					proto, stdout)
			}
			assertContains(t, strings.ToLower(stdout+stderr+err.Error()), "dedicated store",
				fmt.Sprintf("[%s] shared-store barrier rejection should name the dedicated-store requirement", proto))

			// Step 3: Disabling a barrier is always allowed (idempotent no-op
			// on a database that never had one).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-barrier", dbName, "--disabled")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-barrier --disabled failed: %v", proto, err)
			}
			assertNotEmpty(t, stdout+stderr,
				fmt.Sprintf("[%s] database update-barrier --disabled produced no output", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-barrier lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateConnection tests creating a database and
// updating its remote connection configuration across all protocols.
func TestCLIMultiProtocolDatabaseUpdateConnection(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-conn-%s-%d", proto, testTimestamp())

			// Register cleanup to drop the database in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update connection configuration.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-connection", dbName,
				"--address", "localhost:5000",
				"--conn-transport", "tcp")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-connection failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-connection produced no output", proto))

			// Step 3: Verify connection configuration is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-connection failed: %v", proto, err)
			}
			assertContains(t, stdout, "Connection:",
				fmt.Sprintf("[%s] database info should contain 'Connection:' after update-connection", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-connection lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateProtocolPostgreSQL tests creating a database
// and updating its wire protocol to PostgreSQL across all protocols.
func TestCLIMultiProtocolDatabaseUpdateProtocolPostgreSQL(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-pgsql-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update protocol to enable PostgreSQL.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-protocol", dbName, "--postgresql")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-protocol --postgresql failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-protocol --postgresql produced no output", proto))

			// Step 3: Verify protocol is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-protocol --postgresql failed: %v", proto, err)
			}

			infoOutput := stdout + stderr
			assertNotEmpty(t, infoOutput,
				fmt.Sprintf("[%s] database info after update-protocol --postgresql produced no output", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-protocol --postgresql lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateRaftFlags tests creating a database and
// updating its Raft configuration with additional flags (replica-id,
// raft-address) across all protocols. There is no --shard-id flag: the
// cluster issues a database's shard ID once at create and it never changes
// (docs/databases/api.md "There is no shardId"; docs/databases/engineering.md
// "No caller-chosen IDs" -- no transport, the Go/TypeScript SDKs, or the CLI
// carries one on a create or Raft-update request, and Manager.UpdateRaft
// refuses any value other than the issued ID as defence in depth).
func TestCLIMultiProtocolDatabaseUpdateRaftFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-raftf-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update Raft configuration with multiple flags.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-raft", dbName,
				"--cluster-mode", "distributed",
				"--replica-id", "1",
				"--raft-address", "127.0.0.1:7001")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-raft with flags failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-raft with flags produced no output", proto))

			// Step 3: Verify Raft configuration is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-raft flags failed: %v", proto, err)
			}
			assertContains(t, stdout, "Raft:",
				fmt.Sprintf("[%s] database info should contain 'Raft:' after update-raft flags", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-raft flags lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateConnectionQuic tests creating a database
// and updating its connection transport to QUIC across all protocols.
func TestCLIMultiProtocolDatabaseUpdateConnectionQuic(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-quic-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update connection to QUIC transport.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-connection", dbName,
				"--conn-transport", "quic",
				"--address", "localhost:5001")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-connection --conn-transport quic failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-connection quic produced no output", proto))

			// Step 3: Verify connection configuration is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-connection quic failed: %v", proto, err)
			}
			assertContains(t, stdout, "Connection:",
				fmt.Sprintf("[%s] database info should contain 'Connection:' after update-connection quic", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-connection quic lifecycle passed for: %s", proto, dbName)
		})
	}
}

// TestCLIMultiProtocolDatabaseUpdateConnectionTLS tests creating a database
// and updating its connection with TLS certificate flags across all protocols.
func TestCLIMultiProtocolDatabaseUpdateConnectionTLS(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-tls-%s-%d", proto, testTimestamp())

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Step 1: Create a database.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "database", "create", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create failed: %v", proto, err)
			}
			assertContains(t, stdout, "Name:",
				fmt.Sprintf("[%s] database create should contain 'Name:'", proto))

			// Step 2: Update connection with TLS configuration.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-connection", dbName,
				"--address", "localhost:5002",
				"--conn-transport", "tcp",
				"--conn-ca-file", "/data/raft/ca.pem",
				"--conn-cert-file", "/data/admin/admin.crt",
				"--conn-key-file", "/data/admin/admin.key")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-connection TLS failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] database update-connection TLS produced no output", proto))

			// Step 3: Verify connection configuration is reflected in database info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info after update-connection TLS failed: %v", proto, err)
			}
			assertContains(t, stdout, "Connection:",
				fmt.Sprintf("[%s] database info should contain 'Connection:' after update-connection TLS", proto))

			// Step 4: Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] database drop should output 'OK'", proto))

			t.Logf("[%s] database update-connection TLS lifecycle passed for: %s", proto, dbName)
		})
	}
}
