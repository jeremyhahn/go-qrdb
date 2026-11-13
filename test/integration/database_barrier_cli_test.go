// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// database_barrier_cli_test.go drives the newly-wired per-database encryption
// barrier lifecycle (`qrdb database create --barrier-*`, `barrier-status`,
// `seal`, `unseal`) through the real client CLI binary (cmd/qrdb) as a
// subprocess talking REST-over-TLS to a live qrdbd node, reusing the
// none-strategy single-node harness (startCLIEncNode / restConnArgs /
// runQRDB) established by enable_encryption_cli_test.go.
//
// Dedicated-store requirement
// --------------------------------------------------------------------------
// pkg/database.DatabaseConfig.HasDedicatedStore() only reports true for a
// database routed to its own directory (an explicit --data-dir, an
// IsolationVolume tenant's DataRoot, or an IsolationRaft tenant's dedicated
// shard) or backed by BackendRaft. This single-node harness has no tenant
// isolation configured, so the simplest way to get a dedicated store is an
// explicit --data-dir on `database create` -- exactly the "a DB created with
// its own backend dir" case the approved design calls out as supported. A
// plain `database create <name>` with no --data-dir uses the shared default
// store and must be rejected when a barrier is requested
// (SharedStoreBarrierUnsupportedError).
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCLI_DatabaseBarrier_DedicatedStore_Lifecycle drives the full
// create-with-barrier -> kv put -> status -> seal -> kv get (blocked) ->
// status -> unseal(wrong then right) -> kv get (restored) lifecycle for a
// non-raft dedicated-store database, and proves the two things that make the
// per-database barrier a real data-plane control rather than inert
// bookkeeping: (a) the plaintext value written through the live data plane
// never appears on disk under the database's own --data-dir, and (b) a
// sealed barrier blocks live kv reads instead of silently falling back to an
// unencrypted shared store.
func TestCLI_DatabaseBarrier_DedicatedStore_Lifecycle(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startCLIEncNode(t, bin, "[db-barrier-dedicated] ")

	dbName := fmt.Sprintf("dedicated-db-%d", time.Now().UnixNano())
	dataDir := t.TempDir()
	const passphrase = "correct-horse-battery-staple"

	t.Cleanup(func() {
		runQRDB(t, cli, node.connArgs, "database", "drop", dbName) //nolint:errcheck
	})

	// Create a dedicated-store database with a passphrase barrier enabled.
	out, err := runQRDB(t, cli, node.connArgs, "database", "create", dbName,
		"--data-dir", dataDir,
		"--backend", "pebble", "--barrier-enabled",
		"--barrier-strategy", "passphrase",
		"--barrier-passphrase", passphrase,
	)
	if err != nil {
		t.Fatalf("database create: %v\noutput: %s", err, out)
	}

	// The barrier is initialized (and therefore unsealed) as part of create.
	status := cliDatabaseBarrierStatus(t, cli, node.connArgs, dbName)
	if status.Sealed {
		t.Fatalf("barrier-status after create = %+v, want sealed=false", status)
	}
	if status.Strategy != "passphrase" {
		t.Fatalf("barrier-status.strategy after create = %q, want %q", status.Strategy, "passphrase")
	}

	// The database is usable immediately: kv data round-trips through it
	// scoped by --database.
	key, value := "greeting", "hello-dedicated-store-plaintext-marker"
	out, err = runQRDB(t, cli, node.connArgs, "--database", dbName, "kv", "put", key, value)
	if err != nil {
		t.Fatalf("kv put --database %s: %v\noutput: %s", dbName, err, out)
	}
	out, err = runQRDB(t, cli, node.connArgs, "--database", dbName, "kv", "get", key)
	if err != nil {
		t.Fatalf("kv get --database %s: %v\noutput: %s", dbName, err, out)
	}
	if strings.TrimSpace(out) != value {
		t.Fatalf("kv get --database %s %s = %q, want %q", dbName, key, strings.TrimSpace(out), value)
	}

	// Proof (a): the live data plane physically encrypts the database's own
	// dedicated store. The plaintext value must never appear anywhere under
	// its --data-dir -- if it did, the barrier would be inert bookkeeping and
	// requests would be silently served from (or written to) an unencrypted
	// keyspace.
	assertPlaintextAbsentInDir(t, dataDir, []byte(value))

	// Seal it.
	out, err = runQRDB(t, cli, node.connArgs, "database", "seal", dbName)
	if err != nil {
		t.Fatalf("database seal: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("database seal output = %q, want it to contain OK", out)
	}

	status = cliDatabaseBarrierStatus(t, cli, node.connArgs, dbName)
	if !status.Sealed {
		t.Fatal("barrier-status after seal = sealed=false, want true")
	}

	// Proof (b): a sealed database barrier fails closed on the live data
	// plane -- kv get must error instead of returning stale/plaintext data or
	// silently falling back to an unencrypted shared store.
	out, err = runQRDB(t, cli, node.connArgs, "--database", dbName, "kv", "get", key)
	if err == nil {
		t.Fatalf("kv get --database %s while sealed succeeded (output: %s), want error", dbName, out)
	}

	// Wrong secret must fail and leave the barrier sealed.
	out, err = runQRDB(t, cli, node.connArgs, "database", "unseal", dbName, "--secret", "definitely-the-wrong-secret")
	if err == nil {
		t.Fatalf("database unseal with wrong secret succeeded, want error (output: %s)", out)
	}
	status = cliDatabaseBarrierStatus(t, cli, node.connArgs, dbName)
	if !status.Sealed {
		t.Fatal("barrier-status after failed unseal = sealed=false, want true (still sealed)")
	}

	// Correct secret unseals.
	out, err = runQRDB(t, cli, node.connArgs, "database", "unseal", dbName, "--secret", passphrase)
	if err != nil {
		t.Fatalf("database unseal with correct secret: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("database unseal output = %q, want it to contain OK", out)
	}

	status = cliDatabaseBarrierStatus(t, cli, node.connArgs, dbName)
	if status.Sealed {
		t.Fatal("barrier-status after correct-secret unseal = sealed=true, want false")
	}

	// Access is restored: the original value is still there, unchanged.
	out, err = runQRDB(t, cli, node.connArgs, "--database", dbName, "kv", "get", key)
	if err != nil {
		t.Fatalf("kv get --database %s after unseal: %v\noutput: %s", dbName, err, out)
	}
	if strings.TrimSpace(out) != value {
		t.Fatalf("kv get --database %s %s after unseal = %q, want %q", dbName, key, strings.TrimSpace(out), value)
	}
}

// TestCLI_DatabaseBarrier_SharedStore_Rejected verifies that enabling a
// barrier on a database created WITHOUT a dedicated store (no --data-dir,
// non-raft backend -- the default `database create` path) fails closed with
// SharedStoreBarrierUnsupportedError instead of silently creating inert
// barrier state.
func TestCLI_DatabaseBarrier_SharedStore_Rejected(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startCLIEncNode(t, bin, "[db-barrier-shared] ")

	dbName := fmt.Sprintf("shared-db-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		runQRDB(t, cli, node.connArgs, "database", "drop", dbName) //nolint:errcheck
	})

	out, err := runQRDB(t, cli, node.connArgs, "database", "create", dbName,
		"--backend", "pebble", "--barrier-enabled",
		"--barrier-strategy", "passphrase",
		"--barrier-passphrase", "whatever",
	)
	if err == nil {
		t.Fatalf("database create with barrier on a shared-store database succeeded, want error (output: %s)", out)
	}
	combined := strings.ToLower(out + err.Error())
	if !strings.Contains(combined, "dedicated store") {
		t.Fatalf("error should mention %q, got: %s / %v", "dedicated store", out, err)
	}

	// The database must not have been left half-created: a plain create
	// without a barrier now succeeds for the same name.
	out, err = runQRDB(t, cli, node.connArgs, "database", "create", dbName)
	if err != nil {
		t.Fatalf("database create (no barrier) after rejected barrier-create: %v\noutput: %s", err, out)
	}
}

// cliDatabaseBarrierStatus runs `qrdb database barrier-status <name>` with
// JSON output and decodes the result into the same cliBarrierStatusJSON shape
// enable_encryption_cli_test.go uses for `qrdb barrier status` -- the
// database barrier-status response is a superset (it also carries
// hardware_backed / initialized_at), so decoding into the smaller struct is
// safe. It fails the test on a non-zero exit or undecodable output.
func cliDatabaseBarrierStatus(t *testing.T, cli string, connArgs []string, name string) cliBarrierStatusJSON {
	t.Helper()
	out, err := runQRDB(t, cli, connArgs, "--output", "json", "database", "barrier-status", name)
	if err != nil {
		t.Fatalf("qrdb database barrier-status %s: %v\noutput: %s", name, err, out)
	}
	var status cliBarrierStatusJSON
	if decErr := json.Unmarshal([]byte(out), &status); decErr != nil {
		t.Fatalf("decode `qrdb database barrier-status` JSON: %v\noutput: %s", decErr, out)
	}
	return status
}
