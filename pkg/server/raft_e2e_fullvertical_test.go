//go:build qrdb_ts

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

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// This file holds TestRaftE2E_FullVertical alone, under qrdb_ts, while the stack
// it drives (raftE2EStack, newRaftE2EStack, createRaftTenantDB) stays
// unconstrained in raft_e2e_test.go beside TestRaftE2E_ResolveIgnoresLocalCatalog,
// which also uses it. The helper's constraint is therefore a superset of both
// callers' -- the shared machinery moved UP, and only the one test that cannot
// hold untagged moved under the tag.
//
// Why this ONE test is constrained whole, where the other two full-vertical
// tests in this package instead split their time-series leg into the
// assertTimeSeriesLeg pair:
//
// Its subject IS the full vertical across every engine -- that is what its name
// claims and what its assertions are for. Untagged there is no full vertical to
// prove, so any untagged variant would be a different, weaker test wearing this
// one's name. For TestRaftBackendFactory_DataDirUnderTenantRoot and
// TestTenantHost_FullEngineIsolationAndEncryption the time-series round trip is
// an accessory to a different subject (data-dir placement; tenant isolation and
// encryption at rest), so there the leg splits and the subject keeps running in
// both builds.
//
// There is also a hard reason, measured rather than assumed. Untagged, the
// time-series write is refused at SM.Apply, and a refused Apply leaves the shard
// unable to serve ANY subsequent linearizable read: a plain KV.Get on the same
// shard afterwards takes 5m6s and returns "quicraft: timeout" (go-qrdb#520).
// This test reads acme's shard repeatedly after its time-series leg, so untagged
// it cannot merely lose the leg -- it stalls and fails on the reads that follow.
// Reordering the leg to the end would hide that behind an ordering nobody could
// explain from the code.

// TestRaftE2E_FullVertical is the in-process daemon-seam proof for Phase 4b. It
// exercises the real create→resolve→data-plane vertical: an IsolationRaft tenant
// and database are created through DatabaseService.CreateForTenant; KV + MVCC +
// TimeSeries round-trip through the transport-neutral resolver; the state-machine
// Pebble files are proven to live under {DataRoot}/databases/{db}/shard-<N> (NOT
// under the shared host NodeHostDir); data is isolated from the system tenant and
// a second raft tenant; and a restart (shard stop + re-attach) preserves data.
func TestRaftE2E_FullVertical(t *testing.T) {
	stack := newRaftE2EStack(t)
	ctx := context.Background()

	dataRoot, info := stack.createRaftTenantDB(t, "acme", "orders")
	if info.Backend != database.BackendRaft {
		t.Fatalf("database backend = %q, want raft", info.Backend)
	}
	if info.ShardID < shard.UserMin {
		t.Fatalf("shard ID = %d, want >= UserMin", info.ShardID)
	}

	// Data plane via the resolver (the exact seam every transport uses).
	kv, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver KVService: %v", err)
	}
	const secret = "ORDER-PLAINTEXT-42"
	if err := kv.Put(ctx, "order:1", []byte(secret)); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := kv.Get(ctx, "order:1"); err != nil || string(got) != secret {
		t.Fatalf("KV.Get = %q, %v; want %q", got, err, secret)
	}

	mvcc, err := stack.resolver.MVCCService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver MVCCService: %v", err)
	}
	if err := mvcc.Put(ctx, "v:1", []byte("mvcc-val")); err != nil {
		t.Fatalf("MVCC.Put: %v", err)
	}
	if got, err := mvcc.Get(ctx, "v:1"); err != nil || string(got) != "mvcc-val" {
		t.Fatalf("MVCC.Get = %q, %v; want mvcc-val", got, err)
	}

	ts, err := stack.resolver.TimeSeriesService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver TimeSeriesService: %v", err)
	}
	assertTimeSeriesLeg(ctx, t, ts, "", "lat")

	// On-disk proof: SM data under the tenant volume, NOT the shared host dir.
	shardDirName := "shard-" + itoa(info.ShardID)
	smDir := filepath.Join(dataRoot, "databases", "orders", shardDirName)
	if fi, err := os.Stat(smDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected SM data dir %q under tenant DataRoot: %v", smDir, err)
	}
	t.Logf("PROOF: SM Pebble data dir present at %q", smDir)

	// Second raft tenant on its own dedicated shard. (The database.Manager
	// namespaces databases by global name across all isolation modes, so the
	// second tenant uses a distinct name; same-name/different-tenant shard
	// isolation is proven directly at the factory level in
	// TestRaftBackendFactory_PerShardIsolation.)
	root2, info2 := stack.createRaftTenantDB(t, "globex", "invoices")
	if info2.ShardID == info.ShardID {
		t.Fatalf("second raft tenant got the same shard ID %d — not isolated", info2.ShardID)
	}
	kv2, err := stack.resolver.KVService("globex", "invoices")
	if err != nil {
		t.Fatalf("resolver KVService(globex): %v", err)
	}
	if err := kv2.Put(ctx, "order:1", []byte("globex-value")); err != nil {
		t.Fatalf("globex KV.Put: %v", err)
	}
	// acme's value must be unchanged, and the key must not leak across shards.
	if got, err := kv.Get(ctx, "order:1"); err != nil || string(got) != secret {
		t.Fatalf("acme KV.Get after globex write = %q, %v; want %q (isolation breach)", got, err, secret)
	}
	if got, err := kv2.Get(ctx, "order:1"); err != nil || string(got) != "globex-value" {
		t.Fatalf("globex KV.Get = %q, %v; want globex-value", got, err)
	}
	// Cross-tenant leakage check: globex must not see acme's MVCC key.
	if _, err := mvcc.Get(ctx, "v:1"); err != nil {
		t.Fatalf("acme MVCC.Get v:1: %v", err)
	}
	mvcc2, err := stack.resolver.MVCCService("globex", "invoices")
	if err != nil {
		t.Fatalf("resolver MVCCService(globex): %v", err)
	}
	if _, err := mvcc2.Get(ctx, "v:1"); err == nil {
		t.Error("globex MVCC.Get found acme-only key v:1 — isolation breach")
	}
	t.Logf("PROOF: two raft tenants isolated under distinct shards %d/%d", info.ShardID, info2.ShardID)

	// Encryption at rest: stop acme's shard so files flush, scan the tenant
	// volume — the plaintext secret must not appear.
	if err := stack.raftReg.Close("acme", "orders"); err != nil {
		t.Fatalf("stop acme shard: %v", err)
	}
	assertPlaintextAbsent(t, filepath.Join(dataRoot, "databases", "orders"), []byte(secret))
	t.Logf("PROOF: acme data encrypted at rest under %q (plaintext absent)", filepath.Join(dataRoot, "databases", "orders"))

	// Restart proof: re-attach acme's shard at the same DataDir — data persists.
	kvAfter, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver KVService after restart: %v", err)
	}
	if got, err := getWhenReadable(ctx, t, kvAfter.Get, "order:1"); err != nil || string(got) != secret {
		t.Fatalf("acme KV.Get after restart = %q, %v; want %q (data did not persist)", got, err, secret)
	}
	t.Logf("PROOF: acme shard re-attached from tenant volume; data persisted across restart")

	_ = root2
}

// itoa converts a uint64 shard ID to its decimal string without importing strconv
// at call sites; kept local to this proof.
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
