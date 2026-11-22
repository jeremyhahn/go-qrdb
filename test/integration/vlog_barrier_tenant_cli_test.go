// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// vlog_barrier_tenant_cli_test.go drives `qrdb vlog` as a barrier-isolated
// tenant's identity through the real client CLI on every transport. The
// tenant's logs live in its own barrier-sealed home group: while the barrier
// is unsealed every verb works and its proofs verify; once the barrier is
// sealed every verb is refused as sealed; after the unseal the log is still
// there, read back from the same store. A log in a barrier-enabled database
// lives in the database's own barrier backend and is sealed, unsealed and
// dropped with it.
package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

func TestVlog_CLI_BarrierTenant_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVlogBarrierTenantOverProtocol(t, node, proto)
		})
	}
}

// TestVlog_CLI_BarrierTenant_Standalone_PerProtocol drives the same barrier
// tenant's verifiable log as TestVlog_CLI_BarrierTenant_PerProtocol on a
// standalone node: with no home Raft group the tenant's logs are served
// from its standalone tenant barrier store, encrypted under its key and
// refused while its barrier is sealed.
func TestVlog_CLI_BarrierTenant_Standalone_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startStandaloneModeNodeWithConfig(t, "pebble", standaloneBarrierTenantYAML(t.TempDir()))

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVlogBarrierTenantOverProtocol(t, node, proto)
		})
	}
}

// standaloneBarrierTenantYAML admits barrier-isolated tenants with
// unlimited quotas on a standalone node, with a default volume pool at
// volumePoolDir for their barriers.
func standaloneBarrierTenantYAML(volumePoolDir string) string {
	return fmt.Sprintf(`storage:
  volume_pools:
    - name: default
      path: %s
tenant:
  default_entitlements:
    allowed_isolation_modes:
      - prefix
      - barrier
    allow_volume_storage: true
    allow_system_managed_barrier: true
    allowed_volume_pools:
      - default
  default_quotas:
    max_databases: -1
    max_raft_groups: -1
    max_replicas_per_group: -1
    max_total_replicas: -1
    max_dedicated_wal_groups: -1
`, volumePoolDir)
}

// TestVlog_CLI_BarrierDatabase_PerProtocol drives a verifiable log in a
// barrier-enabled database through the real CLI on every transport. The
// log lives in the database's own barrier backend: while the database is
// unsealed every verb works, its proofs verify and none of its entries is in
// the database's data directory in plaintext; once the database is sealed
// every verb is refused as sealed; after the unseal the log is where it was
// and grows; and a database re-created under the same name after a drop has
// no log of the dropped one.
func TestVlog_CLI_BarrierDatabase_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVlogBarrierDatabaseOverProtocol(t, node, proto)
		})
	}
}

// vlogBarrierDatabasePassphrase is the barrier secret of every database
// TestVlog_CLI_BarrierDatabase_PerProtocol creates.
const vlogBarrierDatabasePassphrase = "vlog-barrier-database-passphrase"

// createVlogBarrierDatabase creates dbName as a barrier-enabled pebble
// database whose dedicated store is dataDir.
func createVlogBarrierDatabase(t *testing.T, node *multiProtoNode, proto multiProtocol, dbName, dataDir string) {
	t.Helper()
	if out, err := runCLIProto(t, node, proto, "database", "create", dbName,
		"--data-dir", dataDir,
		"--backend", "pebble", "--barrier-enabled",
		"--barrier-strategy", "passphrase",
		"--barrier-passphrase", vlogBarrierDatabasePassphrase,
	); err != nil {
		t.Fatalf("[%s] database create %s (barrier-enabled): %v\noutput: %s", proto.name, dbName, err, out)
	}
}

func testVlogBarrierDatabaseOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	dbName := fmt.Sprintf("vlogbarrier-%s-%d", proto.name, time.Now().UnixNano())
	dataDir := t.TempDir()
	t.Cleanup(func() {
		_, _ = runCLIProto(t, node, proto, "database", "drop", dbName)
	})
	createVlogBarrierDatabase(t, node, proto, dbName, dataDir)

	logName := "barrier-db-" + proto.name
	entries := make([]string, 6)
	for i := range entries {
		entries[i] = fmt.Sprintf("vlog-barrier-db-%s-entry-%d", proto.name, i)
	}
	assertEntriesEncrypted := func(n int) {
		t.Helper()
		for _, e := range entries[:n] {
			assertPlaintextAbsentInDir(t, dataDir, []byte(e))
		}
	}

	// Unsealed: every verb is served from the database's own barrier
	// backend, the software signing key's sealed record included.
	created := vlogCLIJSON[vlogwire.CreateResult](t, node, proto, "create", "--origin", vlogCLIOrigin, "--database", dbName, "--log", logName, "--signing-key-type", "ed25519")
	if created.Log != logName || created.Database != dbName || created.Key == nil || created.Key.Version != 1 || len(created.Key.PublicKey) == 0 {
		t.Fatalf("[%s] vlog create = %+v, want log %q in %q with a version-1 key and its public key", proto.name, created, logName, dbName)
	}
	vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--database", dbName, "--log", logName, "--entry", entries[0], "--entry", entries[1])
	head2 := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--database", dbName, "--log", logName)
	vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--database", dbName, "--log", logName, "--entry", entries[2], "--entry", entries[3], "--entry", entries[4])
	head5 := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--database", dbName, "--log", logName)
	if head2.Size != 2 || head5.Size != 5 {
		t.Fatalf("[%s] head sizes %d, %d; want 2 and 5", proto.name, head2.Size, head5.Size)
	}
	got := vlogCLIJSON[vlogwire.GetResult](t, node, proto, "get", "--database", dbName, "--log", logName, "--from", "1", "--limit", "3")
	if len(got.Entries) != 3 || string(got.Entries[0].Body) != entries[1] || string(got.Entries[2].Body) != entries[3] {
		t.Fatalf("[%s] get --from 1 --limit 3 = %+v, want entries 1..3", proto.name, got.Entries)
	}
	incl := vlogCLIJSON[vlogwire.InclusionProof](t, node, proto, "prove-inclusion", "--database", dbName, "--log", logName, "--index", "3", "--size", "5")
	if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size, rfc6962.DefaultHasher.HashLeaf([]byte(entries[3])), incl.Hashes, head5.Root); err != nil {
		t.Fatalf("[%s] prove-inclusion of entry 3 does not verify against the size-5 root: %v", proto.name, err)
	}
	cons := vlogCLIJSON[vlogwire.ConsistencyProof](t, node, proto, "prove-consistency", "--database", dbName, "--log", logName, "--size1", "2", "--size2", "5")
	if err := proof.VerifyConsistency(rfc6962.DefaultHasher, cons.Size1, cons.Size2, cons.Hashes, head2.Root, head5.Root); err != nil {
		t.Fatalf("[%s] prove-consistency 2->5 does not verify against the head roots: %v", proto.name, err)
	}
	signed := vlogCLIJSON[vlogwire.SignHeadResult](t, node, proto, "sign-head", "--database", dbName, "--log", logName)
	verifyBarrierCheckpoint(t, proto, created, *created.Key, signed.Checkpoint, 5, head5.Root)
	// The physical proof the log is under the database's key: no entry is
	// in the database's data directory in plaintext.
	assertEntriesEncrypted(5)

	// Sealed: every verb is refused as sealed.
	out, err := runCLIProto(t, node, proto, "database", "seal", dbName)
	if err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("[%s] database seal %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	sealedVerbs := [][]string{
		{"create", "--origin", vlogCLIOrigin, "--database", dbName, "--log", logName + "-sealed"},
		{"append", "--database", dbName, "--log", logName, "--entry", "sealed"},
		{"get", "--database", dbName, "--log", logName, "--from", "0", "--limit", "1"},
		{"head", "--database", dbName, "--log", logName},
		{"prove-inclusion", "--database", dbName, "--log", logName, "--index", "3", "--size", "5"},
		{"prove-consistency", "--database", dbName, "--log", logName, "--size1", "2", "--size2", "5"},
		{"sign-head", "--database", dbName, "--log", logName},
		{"rotate-key", "--database", dbName, "--log", logName, "--signing-key-type", "ecdsa-p256-sha256"},
	}
	for _, args := range sealedVerbs {
		vlogCLIRefused(t, node, proto, "sealed", args...)
	}

	// A wrong secret leaves the database sealed, and the log refused.
	if out, err := runCLIProto(t, node, proto, "database", "unseal", dbName, "--secret", "definitely-wrong"); err == nil {
		t.Fatalf("[%s] database unseal %s with a wrong secret succeeded: %s", proto.name, dbName, out)
	}
	vlogCLIRefused(t, node, proto, "sealed", "head", "--database", dbName, "--log", logName)

	// The correct secret unseals: the log is where it was, unchanged, and grows.
	out, err = runCLIProto(t, node, proto, "database", "unseal", dbName, "--secret", vlogBarrierDatabasePassphrase)
	if err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("[%s] database unseal %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	after := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--database", dbName, "--log", logName)
	if after.Size != 5 || string(after.Root) != string(head5.Root) {
		t.Fatalf("[%s] head after the unseal = size %d root %x; want size 5 root %x", proto.name, after.Size, after.Root, head5.Root)
	}
	appended := vlogCLIJSON[vlogwire.AppendResult](t, node, proto, "append", "--database", dbName, "--log", logName, "--entry", entries[5])
	if appended.FirstIndex != 5 || appended.Count != 1 {
		t.Fatalf("[%s] append after the unseal = %+v, want first index 5, count 1", proto.name, appended)
	}
	head6 := vlogCLIJSON[vlogwire.Head](t, node, proto, "head", "--database", dbName, "--log", logName)
	if head6.Size != 6 {
		t.Fatalf("[%s] head after the append = size %d, want 6", proto.name, head6.Size)
	}
	resigned := vlogCLIJSON[vlogwire.SignHeadResult](t, node, proto, "sign-head", "--database", dbName, "--log", logName)
	verifyBarrierCheckpoint(t, proto, created, *created.Key, resigned.Checkpoint, 6, head6.Root)
	assertEntriesEncrypted(6)

	// Dropped: the log goes with the database. The re-created database has
	// a data directory of its own: drop leaves the caller's directory.
	if out, err := runCLIProto(t, node, proto, "database", "drop", dbName); err != nil {
		t.Fatalf("[%s] database drop %s: %v\noutput: %s", proto.name, dbName, err, out)
	}
	createVlogBarrierDatabase(t, node, proto, dbName, t.TempDir())
	vlogCLIRefused(t, node, proto, "not found", "head", "--database", dbName, "--log", logName)
}

func testVlogBarrierTenantOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	tenantID := fmt.Sprintf("vlb-%s-%d", proto.name, time.Now().UnixNano())

	// A self-managed barrier tenant: its barrier registers sealed and
	// uninitialized; its first admin initializes it (trust-on-first-init).
	adminCert, adminKey := createTenantWithAdminCert(t, node, proto, tenantID, "Vlog Barrier Tenant", "barrier", false, tenantID+"-admin")
	adminConn := tenantCLIArgsForProto(node, proto, adminCert, adminKey)
	if out, err := runCLI(t, node.cli, append(adminConn, "tenant", "barrier-init", "--id", tenantID, "--strategy", "none")...); err != nil {
		t.Fatalf("[%s] barrier-init: %v\noutput: %s", proto.name, err, out)
	}

	certPath, keyPath := node.ca.generateClientCertForTenant(t, tenantID)
	provisionTenantIdentityFor(t, node, tenantID, "vlog-barrier-"+proto.name, append([]string{
		"tenant_barrier:status", "tenant_barrier:seal", "tenant_barrier:unseal", "vlog:sign_head",
	}, vlogTenantPermissions...), certPath)
	conn := tenantCLIArgsForProto(node, proto, certPath, keyPath)
	logName := "barrier-" + proto.name

	// Unsealed: every verb is served from the tenant's own store, the
	// software signing key's sealed record included.
	created := vlogTenantJSON[vlogwire.CreateResult](t, node, proto, conn, "create", "--origin", vlogCLIOrigin, "--log", logName, "--signing-key-type", "ed25519")
	if created.Log != logName || created.Key == nil || created.Key.Version != 1 || len(created.Key.PublicKey) == 0 {
		t.Fatalf("[%s] vlog create = %+v, want log %q with a version-1 key and its public key", proto.name, created, logName)
	}
	vlogTenantJSON[vlogwire.AppendResult](t, node, proto, conn, "append", "--log", logName, "--entry", "e0", "--entry", "e1")
	head2 := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	vlogTenantJSON[vlogwire.AppendResult](t, node, proto, conn, "append", "--log", logName, "--entry", "e2", "--entry", "e3", "--entry", "e4")
	head5 := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	if head2.Size != 2 || head5.Size != 5 {
		t.Fatalf("[%s] head sizes %d, %d; want 2 and 5", proto.name, head2.Size, head5.Size)
	}
	got := vlogTenantJSON[vlogwire.GetResult](t, node, proto, conn, "get", "--log", logName, "--from", "1", "--limit", "3")
	if len(got.Entries) != 3 || string(got.Entries[0].Body) != "e1" || string(got.Entries[2].Body) != "e3" {
		t.Fatalf("[%s] get --from 1 --limit 3 = %+v, want e1..e3", proto.name, got.Entries)
	}
	incl := vlogTenantJSON[vlogwire.InclusionProof](t, node, proto, conn, "prove-inclusion", "--log", logName, "--index", "3", "--size", "5")
	if err := proof.VerifyInclusion(rfc6962.DefaultHasher, incl.Index, incl.Size, rfc6962.DefaultHasher.HashLeaf([]byte("e3")), incl.Hashes, head5.Root); err != nil {
		t.Fatalf("[%s] prove-inclusion of e3 does not verify against the size-5 root: %v", proto.name, err)
	}
	cons := vlogTenantJSON[vlogwire.ConsistencyProof](t, node, proto, conn, "prove-consistency", "--log", logName, "--size1", "2", "--size2", "5")
	if err := proof.VerifyConsistency(rfc6962.DefaultHasher, cons.Size1, cons.Size2, cons.Hashes, head2.Root, head5.Root); err != nil {
		t.Fatalf("[%s] prove-consistency 2->5 does not verify against the head roots: %v", proto.name, err)
	}
	signed := vlogTenantJSON[vlogwire.SignHeadResult](t, node, proto, conn, "sign-head", "--log", logName)
	verifyBarrierCheckpoint(t, proto, created, *created.Key, signed.Checkpoint, 5, head5.Root)

	// Sealed: every verb is refused as sealed, and nothing is written.
	if out, err := runCLI(t, node.cli, append(conn, "tenant", "barrier-seal", "--id", tenantID)...); err != nil {
		t.Fatalf("[%s] barrier-seal: %v\noutput: %s", proto.name, err, out)
	}
	for _, args := range [][]string{
		{"create", "--origin", vlogCLIOrigin, "--log", logName + "-sealed"},
		{"append", "--log", logName, "--entry", "sealed"},
		{"get", "--log", logName, "--from", "0", "--limit", "1"},
		{"head", "--log", logName},
		{"prove-inclusion", "--log", logName, "--index", "3", "--size", "5"},
		{"prove-consistency", "--log", logName, "--size1", "2", "--size2", "5"},
		{"sign-head", "--log", logName},
		{"rotate-key", "--log", logName, "--signing-key-type", "ecdsa-p256-sha256"},
	} {
		out, err := runQRDB(t, node.cli, conn, append([]string{"vlog"}, args...)...)
		if err == nil {
			t.Fatalf("[%s] vlog %s served on a sealed barrier: %s", proto.name, args[0], out)
		}
		if !strings.Contains(strings.ToLower(out), "sealed") {
			t.Fatalf("[%s] vlog %s on a sealed barrier: want a sealed refusal, got: %s", proto.name, args[0], out)
		}
	}

	// Unsealed again: the log is where it was, unchanged.
	if out, err := runCLI(t, node.cli, append(conn, "tenant", "barrier-unseal", "--id", tenantID, "--key", "bm9uZQ==")...); err != nil {
		t.Fatalf("[%s] barrier-unseal: %v\noutput: %s", proto.name, err, out)
	}
	after := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	if after.Size != 5 || string(after.Root) != string(head5.Root) {
		t.Fatalf("[%s] head after the unseal = size %d root %x; want size 5 root %x", proto.name, after.Size, after.Root, head5.Root)
	}

	// The signing key read from the tenant's home group rotates, and the
	// rotated key signs the next checkpoint.
	rotated := vlogTenantJSON[vlogwire.RotateKeyResult](t, node, proto, conn, "rotate-key", "--log", logName, "--signing-key-type", "ecdsa-p256-sha256")
	if rotated.Key.Version != 2 || len(rotated.Key.PublicKey) == 0 || string(rotated.Key.PublicKey) == string(created.Key.PublicKey) {
		t.Fatalf("[%s] vlog rotate-key = %+v, want a new key at version 2", proto.name, rotated.Key)
	}
	vlogTenantJSON[vlogwire.AppendResult](t, node, proto, conn, "append", "--log", logName, "--entry", "e5")
	head6 := vlogTenantJSON[vlogwire.Head](t, node, proto, conn, "head", "--log", logName)
	resigned := vlogTenantJSON[vlogwire.SignHeadResult](t, node, proto, conn, "sign-head", "--log", logName)
	verifyBarrierCheckpoint(t, proto, created, rotated.Key, resigned.Checkpoint, 6, head6.Root)
}

// verifyBarrierCheckpoint verifies note, a checkpoint of created's log, under
// key, and requires it to commit to size and root.
func verifyBarrierCheckpoint(t *testing.T, proto multiProtocol, created vlogwire.CreateResult, key vlogwire.HeadKey, note []byte, size uint64, root []byte) {
	t.Helper()
	pub, err := vlog.HeadKey{Version: key.Version, PublicKey: key.PublicKey}.Public()
	if err != nil {
		t.Fatalf("[%s] key version %d public key: %v", proto.name, key.Version, err)
	}
	keyName := vlog.LogID{TenantID: created.TenantID, Database: created.Database, Log: created.Log}.String()
	got, err := vlog.VerifyCheckpoint(note, vlogCLIOrigin, keyName, pub)
	if err != nil {
		t.Fatalf("[%s] checkpoint does not verify under key version %d: %v\n%s", proto.name, key.Version, err, note)
	}
	if got.Size != size || string(got.Root) != string(root) {
		t.Fatalf("[%s] checkpoint = size %d root %x; want size %d root %x", proto.name, got.Size, got.Root, size, root)
	}
}
