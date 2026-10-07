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

// database_lifecycle_cli_test.go drives the `qrdb database` command tree
// through the real client CLI binary (cmd/qrdb), against every transport
// (REST, gRPC, QUIC, MCP, Unix), using the shared startMultiProtocolNode
// harness (multi_protocol_helpers_test.go). It closes the CLI coverage
// audit's two biggest database-command gaps: `update-protocol`,
// `update-raft`, `update-barrier`, `update-connection`, and `scale` had zero
// integration coverage of any kind, and the whole command tree was
// previously exercised only on the default (REST) transport.
//
// This file deliberately does NOT duplicate database_barrier_cli_test.go's
// deep dedicated-store barrier lifecycle proof (plaintext-never-on-disk,
// restart survival, etc.) -- that semantic depth is already covered on REST.
// TestDatabaseBarrier_CLI_PerProtocol here only proves the same lifecycle is
// reachable and correct over every OTHER transport too.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestDatabaseLifecycle_CLI_PerProtocol drives database create / list / info
// / update-protocol / update-raft / update-barrier / update-connection /
// scale / drop through the CLI binary against every transport, sharing one
// multiProtoNode. Each protocol subtest operates on its own uniquely-named
// database(s) so protocols never contend over the same resource.
func TestDatabaseLifecycle_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testDatabaseLifecycleOverProtocol(t, node, proto)
		})
	}
}

// cliDatabaseInfo runs `qrdb database info <name>` with JSON output and
// decodes the result.
func cliDatabaseInfo(t *testing.T, node *multiProtoNode, proto multiProtocol, name string) transport.DatabaseInfo {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "database", "info", name)
	if err != nil {
		t.Fatalf("[%s] database info %s: %v\noutput: %s", proto.name, name, err, out)
	}
	var info transport.DatabaseInfo
	if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
		t.Fatalf("[%s] decode `database info` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return info
}

// testDatabaseLifecycleOverProtocol exercises database create, list, info,
// update-protocol, update-raft, update-barrier, update-connection, scale,
// and drop against a single protocol.
func testDatabaseLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	dbName := fmt.Sprintf("dblifecycle-%s-%d", proto.name, time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
	})

	// --- create (backend pebble, shared store) -----------
	out, err := runCLIProto(t, node, proto, "database", "create", dbName, "--backend", "pebble")
	if err != nil {
		t.Fatalf("[%s] database create %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	if !strings.Contains(out, dbName) {
		t.Fatalf("[%s] database create %s output = %q, want it to contain the name", proto.name, dbName, out)
	}

	// A duplicate create must fail closed (DatabaseExistsError), not
	// silently succeed or overwrite.
	out, err = runCLIProto(t, node, proto, "database", "create", dbName)
	if err == nil {
		t.Fatalf("[%s] database create %s (duplicate) succeeded, want DatabaseExistsError (output: %s)", proto.name, dbName, out)
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "database", "list")
	if err != nil {
		t.Fatalf("[%s] database list: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, dbName) {
		t.Fatalf("[%s] database list = %q, want it to contain %s", proto.name, out, dbName)
	}

	// --- info --------------------------------------------------------
	info := cliDatabaseInfo(t, node, proto, dbName)
	if info.Name != dbName {
		t.Fatalf("[%s] database info %s: Name = %q, want %q", proto.name, dbName, info.Name, dbName)
	}
	if info.Backend != "pebble" {
		t.Fatalf("[%s] database info %s: Backend = %q, want %q", proto.name, dbName, info.Backend, "pebble")
	}

	// --- update-protocol ---------------------------------------------
	out, err = runCLIProto(t, node, proto, "database", "update-protocol", dbName, "--mysql", "--postgresql")
	if err != nil {
		t.Fatalf("[%s] database update-protocol %s --mysql --postgresql: %v\noutput: %s", proto.name, dbName, err, out)
	}
	info = cliDatabaseInfo(t, node, proto, dbName)
	if !info.Protocol.MySQL || !info.Protocol.PostgreSQL {
		t.Fatalf("[%s] database info %s after update-protocol: Protocol = %+v, want mysql+postgresql enabled", proto.name, dbName, info.Protocol)
	}

	// --- update-raft -------------------------------------------------
	// An unrecognized cluster mode must fail closed.
	out, err = runCLIProto(t, node, proto, "database", "update-raft", dbName, "--cluster-mode", "bogus-mode")
	if err == nil {
		t.Fatalf("[%s] database update-raft %s --cluster-mode bogus-mode succeeded, want error (output: %s)", proto.name, dbName, out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), "cluster mode") {
		t.Fatalf("[%s] database update-raft %s --cluster-mode bogus-mode error should mention %q, got: %s / %v", proto.name, dbName, "cluster mode", out, err)
	}
	// A recognized cluster mode ("none") succeeds and updates the
	// metadata even off a non-raft-backend database -- the server's
	// UpdateRaft does not gate on the database's storage backend.
	out, err = runCLIProto(t, node, proto, "database", "update-raft", dbName, "--cluster-mode", "none")
	if err != nil {
		t.Fatalf("[%s] database update-raft %s --cluster-mode none: %v\noutput: %s", proto.name, dbName, err, out)
	}

	// --- update-barrier ------------------------------------------------
	// Enabling a barrier on a database sharing the default store must be
	// rejected exactly like `database create --barrier-enabled` is
	// (SharedStoreBarrierUnsupportedError).
	out, err = runCLIProto(t, node, proto, "database", "update-barrier", dbName, "--enabled")
	if err == nil {
		t.Fatalf("[%s] database update-barrier %s --enabled (shared store) succeeded, want error (output: %s)", proto.name, dbName, out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), "dedicated store") {
		t.Fatalf("[%s] database update-barrier %s --enabled error should mention %q, got: %s / %v", proto.name, dbName, "dedicated store", out, err)
	}
	// Disabling an already-disabled barrier is a documented no-op and
	// must succeed.
	out, err = runCLIProto(t, node, proto, "database", "update-barrier", dbName, "--disabled")
	if err != nil {
		t.Fatalf("[%s] database update-barrier %s --disabled: %v\noutput: %s", proto.name, dbName, err, out)
	}

	// --- update-connection ---------------------------------------------
	out, err = runCLIProto(t, node, proto, "database", "update-connection", dbName,
		"--conn-transport", "tcp", "--address", "127.0.0.1:59999")
	if err != nil {
		t.Fatalf("[%s] database update-connection %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	info = cliDatabaseInfo(t, node, proto, dbName)
	if info.Connection == nil || info.Connection.Address != "127.0.0.1:59999" || info.Connection.Transport != "tcp" {
		t.Fatalf("[%s] database info %s after update-connection: Connection = %+v, want {tcp 127.0.0.1:59999}", proto.name, dbName, info.Connection)
	}
	// An empty address must fail closed (ConnectionRequiredError).
	out, err = runCLIProto(t, node, proto, "database", "update-connection", dbName, "--conn-transport", "tcp")
	if err == nil {
		t.Fatalf("[%s] database update-connection %s with no --address succeeded, want error (output: %s)", proto.name, dbName, out)
	}

	// --- scale -------------------------------------------------------
	// This harness's CLI identity (CN=admin, OU=system) resolves to the
	// system tenant, and pkg/service.DatabaseService.ScaleForTenant
	// rejects the system tenant outright (TenantIDRequiredError) before it
	// ever reaches the placement-subsystem / dedicated-raft checks --
	// scaling is a per-tenant dedicated-raft operation with no
	// system-tenant counterpart. This is the real, documented, standalone
	// behavior for a system-scoped admin identity, and it is identical on
	// every transport.
	out, err = runCLIProto(t, node, proto, "database", "scale", dbName, "--replicas", "3")
	if err == nil {
		t.Fatalf("[%s] database scale %s --replicas 3 succeeded, want TenantIDRequiredError (output: %s)", proto.name, dbName, out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), "tenant id required") {
		t.Fatalf("[%s] database scale %s error should mention %q, got: %s / %v", proto.name, dbName, "tenant id required", out, err)
	}
	// `scale` requires --replicas; cobra must reject the invocation
	// before it ever reaches the server.
	out, err = runCLIProto(t, node, proto, "database", "scale", dbName)
	if err == nil {
		t.Fatalf("[%s] database scale %s with no --replicas succeeded, want error (output: %s)", proto.name, dbName, out)
	}

	// --- update-compaction --------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "database", "update-compaction", dbName,
		"--mode", "revision", "--retain-revisions", "7")
	if err != nil {
		t.Fatalf("[%s] database update-compaction %s --mode revision: %v\noutput: %s", proto.name, dbName, err, out)
	}
	var afterCompaction transport.DatabaseInfo
	if jerr := json.Unmarshal([]byte(out), &afterCompaction); jerr != nil {
		t.Fatalf("[%s] decode database update-compaction JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if afterCompaction.Compaction == nil || afterCompaction.Compaction.Mode != "revision" || afterCompaction.Compaction.RetainRevisions != 7 {
		t.Fatalf("[%s] database update-compaction %s: compaction = %+v, want mode=revision retainRevisions=7", proto.name, dbName, afterCompaction.Compaction)
	}
	// The policy round-trips through `database info` too, not just the
	// update response.
	info = cliDatabaseInfo(t, node, proto, dbName)
	if info.Compaction == nil || info.Compaction.Mode != "revision" || info.Compaction.RetainRevisions != 7 {
		t.Fatalf("[%s] database info %s after update-compaction: compaction = %+v, want mode=revision retainRevisions=7", proto.name, dbName, info.Compaction)
	}
	// --mode is required.
	out, err = runCLIProto(t, node, proto, "database", "update-compaction", dbName)
	if err == nil {
		t.Fatalf("[%s] database update-compaction %s with no --mode succeeded, want error (output: %s)", proto.name, dbName, out)
	}
	// Turn it back off so the drop below exercises the default state.
	out, err = runCLIProto(t, node, proto, "--output", "json", "database", "update-compaction", dbName, "--mode", "off")
	if err != nil {
		t.Fatalf("[%s] database update-compaction %s --mode off: %v\noutput: %s", proto.name, dbName, err, out)
	}

	// --- drop --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "database", "drop", dbName)
	if err != nil {
		t.Fatalf("[%s] database drop %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] database drop %s: expected OK, got: %s", proto.name, dbName, out)
	}
	out, err = runCLIProto(t, node, proto, "database", "info", dbName)
	if err == nil {
		t.Fatalf("[%s] database info %s after drop succeeded, want NotFoundError (output: %s)", proto.name, dbName, out)
	}
	if !strings.Contains(strings.ToLower(out+err.Error()), "not found") {
		t.Fatalf("[%s] database info %s after drop error should mention %q, got: %s / %v", proto.name, dbName, "not found", out, err)
	}
}

// TestDatabaseLifecycle_CLI_BackendVariants exercises `database create`'s
// backend permutations (memory, dedicated-raft with --replicas) once each on
// the REST transport, since these are flag-surface checks rather than
// transport-parity checks -- the per-protocol breadth for the default
// (pebble, shared-store) path is already proven by
// TestDatabaseLifecycle_CLI_PerProtocol.
func TestDatabaseLifecycle_CLI_BackendVariants(t *testing.T) {
	node := startMultiProtocolNode(t)
	proto := multiProtocols[0] // rest

	t.Run("memory_backend", func(t *testing.T) {
		dbName := fmt.Sprintf("dbmem-%d", time.Now().UnixNano())
		t.Cleanup(func() { _, _ = runCLIProto(t, node, proto, "database", "drop", dbName) })

		out, err := runCLIProto(t, node, proto, "database", "create", dbName, "--backend", "memory")
		if err != nil {
			t.Fatalf("database create %s --backend memory: %v\noutput: %s", dbName, err, out)
		}
		info := cliDatabaseInfo(t, node, proto, dbName)
		if info.Backend != "memory" {
			t.Fatalf("database info %s: Backend = %q, want %q", dbName, info.Backend, "memory")
		}

		out, err = runCLIProto(t, node, proto, "database", "drop", dbName)
		if err != nil {
			t.Fatalf("database drop %s: %v\noutput: %s", dbName, err, out)
		}
	})

	t.Run("dedicated_raft_backend_with_replicas", func(t *testing.T) {
		dbName := fmt.Sprintf("dbraft-%d", time.Now().UnixNano())
		t.Cleanup(func() { _, _ = runCLIProto(t, node, proto, "database", "drop", dbName) })

		out, err := runCLIProto(t, node, proto, "database", "create", dbName,
			"--backend", "raft", "--cluster-mode", "local", "--replicas", "1")
		if err != nil {
			t.Fatalf("database create %s --backend raft --cluster-mode local --replicas 1: %v\noutput: %s", dbName, err, out)
		}
		info := cliDatabaseInfo(t, node, proto, dbName)
		if info.Backend != "raft" {
			t.Fatalf("database info %s: Backend = %q, want %q", dbName, info.Backend, "raft")
		}
		if info.Raft == nil || info.Raft.ClusterMode != "local" {
			t.Fatalf("database info %s: Raft = %+v, want ClusterMode=local", dbName, info.Raft)
		}

		out, err = runCLIProto(t, node, proto, "database", "drop", dbName)
		if err != nil {
			t.Fatalf("database drop %s: %v\noutput: %s", dbName, err, out)
		}
	})
}

// TestDatabaseBarrier_CLI_PerProtocol drives create-with-barrier ->
// barrier-status -> seal -> barrier-status -> unseal(wrong) ->
// unseal(right) -> barrier-status -> drop through the CLI binary against
// every transport, mirroring TestAdminDrain_PerProtocol's isolated-resource-
// per-protocol pattern: each subtest creates its OWN dedicated-store
// database (a fresh --data-dir) so protocols never contend over the same
// barrier.
func TestDatabaseBarrier_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testDatabaseBarrierOverProtocol(t, node, proto)
		})
	}
}

// cliDatabaseBarrierStatusProto runs `database barrier-status <name>` over
// the given protocol with JSON output and decodes the result.
func cliDatabaseBarrierStatusProto(t *testing.T, node *multiProtoNode, proto multiProtocol, name string) cliBarrierStatusJSON {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "database", "barrier-status", name)
	if err != nil {
		t.Fatalf("[%s] database barrier-status %s: %v\noutput: %s", proto.name, name, err, out)
	}
	var status cliBarrierStatusJSON
	if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
		t.Fatalf("[%s] decode `database barrier-status` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return status
}

// testDatabaseBarrierOverProtocol exercises the per-database encryption
// barrier lifecycle against a single protocol.
func testDatabaseBarrierOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	dbName := fmt.Sprintf("dbbarrier-%s-%d", proto.name, time.Now().UnixNano())
	dataDir := t.TempDir()
	const passphrase = "db-barrier-cli-mp-passphrase"
	t.Cleanup(func() {
		_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
	})

	out, err := runCLIProto(t, node, proto, "database", "create", dbName,
		"--data-dir", dataDir,
		"--barrier-enabled",
		"--barrier-strategy", "passphrase",
		"--barrier-passphrase", passphrase,
	)
	if err != nil {
		t.Fatalf("[%s] database create %s (barrier-enabled): %v\noutput: %s", proto.name, dbName, err, out)
	}

	status := cliDatabaseBarrierStatusProto(t, node, proto, dbName)
	if status.Sealed {
		t.Fatalf("[%s] barrier-status after create = %+v, want sealed=false", proto.name, status)
	}
	if status.Strategy != "passphrase" {
		t.Fatalf("[%s] barrier-status after create: strategy = %q, want %q", proto.name, status.Strategy, "passphrase")
	}

	out, err = runCLIProto(t, node, proto, "database", "seal", dbName)
	if err != nil {
		t.Fatalf("[%s] database seal %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] database seal %s: expected OK, got: %s", proto.name, dbName, out)
	}
	status = cliDatabaseBarrierStatusProto(t, node, proto, dbName)
	if !status.Sealed {
		t.Fatalf("[%s] barrier-status after seal = %+v, want sealed=true", proto.name, status)
	}

	// Wrong secret must fail and leave the barrier sealed.
	out, err = runCLIProto(t, node, proto, "database", "unseal", dbName, "--secret", "definitely-the-wrong-secret")
	if err == nil {
		t.Fatalf("[%s] database unseal %s with wrong secret succeeded, want error (output: %s)", proto.name, dbName, out)
	}
	status = cliDatabaseBarrierStatusProto(t, node, proto, dbName)
	if !status.Sealed {
		t.Fatalf("[%s] barrier-status after failed unseal = %+v, want sealed=true (still sealed)", proto.name, status)
	}

	// Correct secret unseals.
	out, err = runCLIProto(t, node, proto, "database", "unseal", dbName, "--secret", passphrase)
	if err != nil {
		t.Fatalf("[%s] database unseal %s with correct secret: %v\noutput: %s", proto.name, dbName, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] database unseal %s: expected OK, got: %s", proto.name, dbName, out)
	}
	status = cliDatabaseBarrierStatusProto(t, node, proto, dbName)
	if status.Sealed {
		t.Fatalf("[%s] barrier-status after correct-secret unseal = %+v, want sealed=false", proto.name, status)
	}
}
