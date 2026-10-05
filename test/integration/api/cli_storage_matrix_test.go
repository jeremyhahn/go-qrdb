// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIStorageMatrixLifecycle tests the full database management lifecycle
// (create, info, list, drop) for every valid combination of backend (memory,
// file, pebble) x engine (kv, timeseries) x protocol (unix, rest, grpc, mcp,
// quic). The raft backend is excluded because it requires a multi-node cluster
// that is not available in the single-node integration environment.
func TestCLIStorageMatrixLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	backends := []string{"memory", "file", "pebble"}
	engines := []string{"kv", "timeseries"}

	for _, backend := range backends {
		for _, engine := range engines {
			for _, proto := range AllProtocols() {
				t.Run(fmt.Sprintf("%s/%s/%s", backend, engine, proto), func(t *testing.T) {
					if !cfg.IsProtocolAvailable(t, proto) {
						t.Fatalf("server not available for protocol %s", proto)
					}

					// Skip timeseries engine if the feature is not available.
					if engine == "timeseries" {
						requireFeature(t, cfg, "ts")
					}

					dbName := fmt.Sprintf("mx-%s-%s-%s-%d", backend, engine, proto, testTimestamp())

					// Register cleanup before any mutations.
					t.Cleanup(func() {
						execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
					})

					// Step 1: Create a database with the specified backend and engine.
					stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
						"database", "create", dbName,
						"--backend", backend,
						"--engine", engine)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] database create --backend %s --engine %s failed: %v", proto, backend, engine, err)
					}
					assertContains(t, stdout, "Name:",
						fmt.Sprintf("[%s] database create should contain 'Name:'", proto))
					t.Logf("[%s] created database %s with backend=%s engine=%s", proto, dbName, backend, engine)

					// Step 2: Verify info output contains correct backend and engine.
					stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] database info failed: %v", proto, err)
					}
					assertContains(t, strings.ToLower(stdout), backend,
						fmt.Sprintf("[%s] database info should contain backend '%s'", proto, backend))
					assertContains(t, strings.ToLower(stdout), engine,
						fmt.Sprintf("[%s] database info should contain engine '%s'", proto, engine))

					// Step 3: Verify list includes the database.
					stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "list")
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] database list failed: %v", proto, err)
					}
					assertContains(t, stdout, dbName,
						fmt.Sprintf("[%s] database list should contain '%s'", proto, dbName))

					// Step 4: Drop the database.
					stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] database drop failed: %v", proto, err)
					}
					assertContains(t, stdout, "OK",
						fmt.Sprintf("[%s] database drop should output 'OK'", proto))

					// Step 5: Verify list no longer includes the database.
					stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "list")
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] database list after drop failed: %v", proto, err)
					}
					if strings.Contains(stdout, dbName) {
						t.Fatalf("[%s] database list should not contain '%s' after drop", proto, dbName)
					}

					t.Logf("[%s] storage matrix lifecycle passed for backend=%s engine=%s", proto, backend, engine)
				})
			}
		}
	}
}

// TestCLIStorageMatrixUpdateOperations tests that all database update commands
// work correctly for each backend x protocol combination. For each backend, a
// database is created and then every update operation is applied and verified
// via database info output.
func TestCLIStorageMatrixUpdateOperations(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	backends := []string{"memory", "file", "pebble"}

	// etcdPortBase/etcdPort give each (backend, protocol) combination its own
	// listen port. A port shared across the matrix's 15 combinations raced
	// the previous subtest's etcd listener teardown (database drop) against
	// this one's bind, which does not always lose: "address already in use"
	// on update-etcd --listen-addr localhost:2379 is exactly that race, not
	// a per-subtest defect. A distinct port per combination removes the
	// shared resource instead of trying to win the race.
	const etcdPortBase = 23790
	etcdPort := etcdPortBase

	for _, backend := range backends {
		for _, proto := range AllProtocols() {
			etcdPort++
			listenAddr := fmt.Sprintf("localhost:%d", etcdPort)
			t.Run(fmt.Sprintf("%s/%s", backend, proto), func(t *testing.T) {
				if !cfg.IsProtocolAvailable(t, proto) {
					t.Fatalf("server not available for protocol %s", proto)
				}

				dbName := fmt.Sprintf("upd-%s-%s-%d", backend, proto, testTimestamp())

				// Register cleanup before any mutations.
				t.Cleanup(func() {
					execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
				})

				// Create the database with the specified backend.
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"database", "create", dbName,
					"--backend", backend)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database create --backend %s failed: %v", proto, backend, err)
				}
				assertContains(t, stdout, "Name:",
					fmt.Sprintf("[%s] database create should contain 'Name:'", proto))
				t.Logf("[%s] created database %s with backend=%s", proto, dbName, backend)

				// Update 1: update-protocol --mysql
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
				t.Logf("[%s] update-protocol --mysql passed", proto)

				// Verify update-protocol via info.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database info after update-protocol failed: %v", proto, err)
				}
				assertNotEmpty(t, stdout+stderr,
					fmt.Sprintf("[%s] database info after update-protocol produced no output", proto))

				// Update 2: update-etcd --enabled --listen-addr, on this
				// combination's own port (see listenAddr's doc comment above).
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"database", "update-etcd", dbName,
					"--enabled",
					"--listen-addr", listenAddr)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database update-etcd failed: %v", proto, err)
				}
				output = stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] database update-etcd produced no output", proto))
				t.Logf("[%s] update-etcd --enabled passed", proto)

				// Verify update-etcd via info.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database info after update-etcd failed: %v", proto, err)
				}
				assertContains(t, stdout, "Etcd:",
					fmt.Sprintf("[%s] database info should contain 'Etcd:' after update-etcd", proto))

				// Update 3: update-raft --cluster-mode local --replica-id 1.
				// No --shard-id: the cluster issues a database's shard ID
				// once at create and it is never caller-settable
				// (docs/databases/api.md "There is no shardId").
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"database", "update-raft", dbName,
					"--cluster-mode", "local",
					"--replica-id", "1")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database update-raft failed: %v", proto, err)
				}
				output = stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] database update-raft produced no output", proto))
				t.Logf("[%s] update-raft --cluster-mode local passed", proto)

				// Verify update-raft via info.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database info after update-raft failed: %v", proto, err)
				}
				assertContains(t, stdout, "Raft:",
					fmt.Sprintf("[%s] database info should contain 'Raft:' after update-raft", proto))

				// Update 4: update-barrier --enabled fails closed on this
				// shared-store database (a per-database barrier is only real
				// on a dedicated store; see cli_database_test.go). The
				// dedicated-store happy path lives in the cluster-lane
				// TestDatabaseBarrier* tests.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"database", "update-barrier", dbName, "--enabled")
				if err == nil {
					t.Fatalf("[%s] update-barrier --enabled on a shared-store database must fail closed, got success (stdout: %s)",
						proto, stdout)
				}
				assertContains(t, strings.ToLower(stdout+stderr+err.Error()), "dedicated store",
					fmt.Sprintf("[%s] shared-store barrier rejection should name the dedicated-store requirement", proto))
				t.Logf("[%s] update-barrier --enabled correctly rejected on shared store", proto)

				// Update 5: update-connection --conn-transport tcp --address remote:9000
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"database", "update-connection", dbName,
					"--conn-transport", "tcp",
					"--address", "remote:9000")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database update-connection failed: %v", proto, err)
				}
				output = stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] database update-connection produced no output", proto))
				t.Logf("[%s] update-connection --transport tcp passed", proto)

				// Verify update-connection via info.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database info after update-connection failed: %v", proto, err)
				}
				assertContains(t, stdout, "Connection:",
					fmt.Sprintf("[%s] database info should contain 'Connection:' after update-connection", proto))

				// Drop the database.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database drop failed: %v", proto, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] database drop should output 'OK'", proto))

				t.Logf("[%s] storage matrix update operations passed for backend=%s", proto, backend)
			})
		}
	}
}

// TestCLIStorageMatrixBackendVerification verifies that the backend reported
// by database info matches the backend requested at creation time. Each
// backend type is tested across all protocols to ensure the backend value is
// correctly persisted and reported.
func TestCLIStorageMatrixBackendVerification(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	backends := []string{"memory", "file", "pebble"}

	for _, backend := range backends {
		for _, proto := range AllProtocols() {
			t.Run(fmt.Sprintf("%s/%s", backend, proto), func(t *testing.T) {
				if !cfg.IsProtocolAvailable(t, proto) {
					t.Fatalf("server not available for protocol %s", proto)
				}

				dbName := fmt.Sprintf("bkv-%s-%s-%d", backend, proto, testTimestamp())

				// Register cleanup before any mutations.
				t.Cleanup(func() {
					execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
				})

				// Create database with specific backend.
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"database", "create", dbName,
					"--backend", backend)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database create --backend %s failed: %v", proto, backend, err)
				}
				t.Logf("[%s] created database %s with backend=%s", proto, dbName, backend)

				// Verify info shows the correct backend.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database info failed: %v", proto, err)
				}
				assertContains(t, stdout, "Backend:",
					fmt.Sprintf("[%s] database info should contain 'Backend:'", proto))
				assertContains(t, strings.ToLower(stdout), backend,
					fmt.Sprintf("[%s] database info Backend should be '%s'", proto, backend))

				// Drop the database.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database drop failed: %v", proto, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] database drop should output 'OK'", proto))

				t.Logf("[%s] backend verification passed for backend=%s", proto, backend)
			})
		}
	}
}

// TestCLIStorageMatrixEngineVerification verifies that the engine reported by
// database info matches the engine requested at creation time. Each engine
// type is tested across all protocols to ensure the engine value is correctly
// persisted and reported.
func TestCLIStorageMatrixEngineVerification(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	engines := []string{"kv", "timeseries"}

	for _, engine := range engines {
		for _, proto := range AllProtocols() {
			t.Run(fmt.Sprintf("%s/%s", engine, proto), func(t *testing.T) {
				if !cfg.IsProtocolAvailable(t, proto) {
					t.Fatalf("server not available for protocol %s", proto)
				}

				// Skip timeseries engine if the feature is not available.
				if engine == "timeseries" {
					requireFeature(t, cfg, "ts")
				}

				dbName := fmt.Sprintf("eng-%s-%s-%d", engine, proto, testTimestamp())

				// Register cleanup before any mutations.
				t.Cleanup(func() {
					execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
				})

				// Create database with specific engine.
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"database", "create", dbName,
					"--engine", engine)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database create --engine %s failed: %v", proto, engine, err)
				}
				t.Logf("[%s] created database %s with engine=%s", proto, dbName, engine)

				// Verify info shows the correct engine.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database info failed: %v", proto, err)
				}
				assertContains(t, stdout, "Engine:",
					fmt.Sprintf("[%s] database info should contain 'Engine:'", proto))
				assertContains(t, strings.ToLower(stdout), engine,
					fmt.Sprintf("[%s] database info Engine should be '%s'", proto, engine))

				// Drop the database.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] database drop failed: %v", proto, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] database drop should output 'OK'", proto))

				t.Logf("[%s] engine verification passed for engine=%s", proto, engine)
			})
		}
	}
}

// TestCLIStorageMatrixInvalidBackend tests that creating a database with an
// invalid backend name fails gracefully and returns an error.
func TestCLIStorageMatrixInvalidBackend(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("bad-be-%s-%d", proto, testTimestamp())

			// Register cleanup in case the create unexpectedly succeeds.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Attempt to create a database with an invalid backend.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "create", dbName,
				"--backend", "invalidbackend")
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create --backend invalidbackend should have failed but succeeded", proto)
			}

			t.Logf("[%s] database create --backend invalidbackend correctly failed: %v", proto, err)
		})
	}
}

// TestCLIStorageMatrixInvalidEngine tests that creating a database with an
// invalid engine name fails gracefully and returns an error.
func TestCLIStorageMatrixInvalidEngine(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("bad-eng-%s-%d", proto, testTimestamp())

			// Register cleanup in case the create unexpectedly succeeds.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Attempt to create a database with an invalid engine.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "create", dbName,
				"--engine", "invalidengine")
			if err == nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create --engine invalidengine should have failed but succeeded", proto)
			}

			t.Logf("[%s] database create --engine invalidengine correctly failed: %v", proto, err)
		})
	}
}

// TestCLIStorageMatrixRaftBackendRequiresCluster tests that creating a database
// with --backend raft without proper cluster configuration either fails or
// behaves in a documented manner. In the single-node integration environment,
// the raft backend is not expected to succeed without multi-node cluster setup.
func TestCLIStorageMatrixRaftBackendRequiresCluster(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("raft-be-%s-%d", proto, testTimestamp())

			// Register cleanup in case the create unexpectedly succeeds.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			// Attempt to create a database with raft backend without cluster config.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "create", dbName,
				"--backend", "raft")

			if err != nil {
				// Expected: raft backend fails without proper cluster setup.
				t.Logf("[%s] database create --backend raft correctly failed without cluster config: %v", proto, err)
				t.Logf("[%s] stdout: %s", proto, stdout)
				t.Logf("[%s] stderr: %s", proto, stderr)
				return
			}

			// If the create succeeded, verify the database was created and clean up.
			// This documents the case where the server accepts raft backend creation
			// in single-node mode (potentially deferring cluster validation).
			t.Logf("[%s] database create --backend raft succeeded in single-node mode", proto)
			t.Logf("[%s] stdout: %s", proto, stdout)

			// Verify the backend is reported as raft.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database info failed: %v", proto, err)
			}
			assertContains(t, strings.ToLower(stdout), "raft",
				fmt.Sprintf("[%s] database info should contain 'raft' backend", proto))

			// Drop the database.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database drop failed: %v", proto, err)
			}

			t.Logf("[%s] raft backend cluster requirement test passed", proto)
		})
	}
}
