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

package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// migrateTestTenantShard gives hostReg a raft backend factory, as the server
// wires it, and starts one dedicated raft shard of tenantID with its own WAL
// under the tenant's DataRoot: the home group an IsolationVolume tenant runs
// beside its Host, under the same tenant barrier.
func migrateTestTenantShard(t *testing.T, reg *seal.BarrierRegistry, hostReg *TenantHostRegistry, tenantID, dataRoot, db string, shardID uint64) *RaftBackendFactory {
	t.Helper()
	host, mux := newSharedTestHostWithMux(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })
	hostReg.raftShards.Store(f)

	src.add(tenantID, db, shardID, dataRoot)
	src.setDedicated(tenantID, db)
	if _, err := f.Services(context.Background(), tenantID, db); err != nil {
		t.Fatalf("Services(%s/%s): %v", tenantID, db, err)
	}
	return f
}

// TestTenantHostRegistry_MigrateEncryption_RecodesTenantShards proves the
// recycle covers the dedicated raft shard an IsolationVolume tenant runs beside
// its Host: the shard's state machine and dedicated WAL are re-encrypted with
// the Host's stores, so the shard starts again under the keyed barrier with
// every datum intact, keeps committing, and leaves no plaintext on disk.
func TestTenantHostRegistry_MigrateEncryption_RecodesTenantShards(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, dataRoot := migrateTestTenant(t, "vt")
	f := migrateTestTenantShard(t, reg, hostReg, "vt", dataRoot, "home", 2300)

	shard, err := f.Services(ctx, "vt", "home")
	if err != nil {
		t.Fatalf("Services(shard, pre-migrate): %v", err)
	}
	if err := shard.KV.Put(ctx, "shard-key", []byte("shard-plaintext-value")); err != nil {
		t.Fatalf("shard KV.Put: %v", err)
	}
	svcs, err := hostReg.Services(ctx, "vt")
	if err != nil {
		t.Fatalf("Services(host, pre-migrate): %v", err)
	}
	if err := svcs.KV.Put(ctx, "kv-key", []byte("kv-value")); err != nil {
		t.Fatalf("host KV.Put: %v", err)
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("tenant-migrate-pw")),
	}
	if err := hostReg.MigrateEncryption(ctx, "vt", opts); err != nil {
		t.Fatalf("MigrateEncryption: %v", err)
	}
	if got := tenantStrategy(t, reg, "vt"); got != seal.StrategyPassphrase {
		t.Fatalf("post-migrate tenant strategy = %q, want passphrase", got)
	}

	started := false
	for _, s := range f.ListStarted() {
		started = started || (s.TenantID == "vt" && s.Database == "home")
	}
	if !started {
		t.Fatal("the tenant's shard was not started again after the migration")
	}
	shard, err = f.Services(ctx, "vt", "home")
	if err != nil {
		t.Fatalf("Services(shard, post-migrate): %v", err)
	}
	if got, gerr := shard.KV.Get(ctx, "shard-key"); gerr != nil || string(got) != "shard-plaintext-value" {
		t.Fatalf("shard KV.Get(post-migrate) = %q, %v; want shard-plaintext-value (DATA LOSS)", got, gerr)
	}
	if err := shard.KV.Put(ctx, "shard-key2", []byte("after")); err != nil {
		t.Fatalf("shard KV.Put(post-migrate): %v", err)
	}
	if got, gerr := shard.KV.Get(ctx, "shard-key2"); gerr != nil || string(got) != "after" {
		t.Fatalf("shard KV.Get(post-migrate new) = %q, %v; want after", got, gerr)
	}
	svcs, err = hostReg.Services(ctx, "vt")
	if err != nil {
		t.Fatalf("Services(host, post-migrate): %v", err)
	}
	if got, gerr := svcs.KV.Get(ctx, "kv-key"); gerr != nil || string(got) != "kv-value" {
		t.Fatalf("host KV.Get(post-migrate) = %q, %v; want kv-value", got, gerr)
	}

	if err := f.Close("vt", "home"); err != nil {
		t.Fatalf("stop shard: %v", err)
	}
	assertPlaintextAbsent(t, filepath.Join(dataRoot, "databases", "home"), []byte("shard-plaintext-value"))
}

// TestTenantHostRegistry_MigrateEncryption_ShamirLeavesShardsStopped proves a
// Shamir target, whose keyed barrier reopens sealed pending its holders'
// quorum, completes without reporting the shard's sealed start as a failure:
// the shard stays stopped, and once a quorum unseals the barrier it starts
// under the keyed barrier with its pre-migration data.
func TestTenantHostRegistry_MigrateEncryption_ShamirLeavesShardsStopped(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, dataRoot := migrateTestTenant(t, "vt-shamir")
	tb, err := reg.GetTenant("vt-shamir")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	custody := sealtest.NewCustody("h1", "h2", "h3")
	custody.Install(tb.Barrier())
	f := migrateTestTenantShard(t, reg, hostReg, "vt-shamir", dataRoot, "home", 2301)

	shard, err := f.Services(ctx, "vt-shamir", "home")
	if err != nil {
		t.Fatalf("Services(shard, pre-migrate): %v", err)
	}
	if err := shard.KV.Put(ctx, "shard-key", []byte("shard-value")); err != nil {
		t.Fatalf("shard KV.Put: %v", err)
	}
	if _, err := hostReg.Services(ctx, "vt-shamir"); err != nil {
		t.Fatalf("Services(host, pre-migrate): %v", err)
	}

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	if err := hostReg.MigrateEncryption(ctx, "vt-shamir", opts); err != nil {
		t.Fatalf("MigrateEncryption(shamir): %v", err)
	}
	if _, err := f.Services(ctx, "vt-shamir", "home"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services(shard) on the sealed migrated barrier = %v, want seal.ErrSealed", err)
	}

	keyed, err := reg.GetTenant("vt-shamir")
	if err != nil {
		t.Fatalf("GetTenant(post-migrate): %v", err)
	}
	if _, err := custody.Quorum(ctx, keyed.Barrier(), seal.DomainUnseal, seal.OpUnseal, "h1", "h2"); err != nil {
		t.Fatalf("unseal quorum: %v", err)
	}
	if err := f.Attach(ctx, "vt-shamir", "home"); err != nil {
		t.Fatalf("Attach(shard, post-unseal): %v", err)
	}
	shard, err = f.Services(ctx, "vt-shamir", "home")
	if err != nil {
		t.Fatalf("Services(shard, post-unseal): %v", err)
	}
	if got, gerr := shard.KV.Get(ctx, "shard-key"); gerr != nil || string(got) != "shard-value" {
		t.Fatalf("shard KV.Get(post-unseal) = %q, %v; want shard-value (DATA LOSS)", got, gerr)
	}
}

// TestTenantShardRecode_NilIsNoop proves a migration on a node without a raft
// backend factory needs no special case: every step of the nil recode does
// nothing and reports success.
func TestTenantShardRecode_NilIsNoop(t *testing.T) {
	ctx := context.Background()
	var f *RaftBackendFactory
	r := f.beginTenantShardRecode("t")
	if r != nil {
		t.Fatalf("beginTenantShardRecode on a nil factory = %v, want nil", r)
	}
	if !r.Empty() || r.StoreDirs() != nil {
		t.Fatalf("nil recode: Empty=%v StoreDirs=%v, want true and nil", r.Empty(), r.StoreDirs())
	}
	fail := func([]database.StagedSnapshot) error { return errors.New("checkpoint must not run") }
	for name, step := range map[string]func() error{
		"Quiesce":         r.Quiesce,
		"Copy":            func() error { return r.Copy(ctx, nil, nil, nil, fail) },
		"Verify":          func() error { return r.Verify(ctx, nil, nil) },
		"CommitSnapshots": func() error { return r.CommitSnapshots(ctx) },
		"Release":         func() error { return r.Release(ctx, false) },
	} {
		if err := step(); err != nil {
			t.Fatalf("nil recode %s = %v, want nil", name, err)
		}
	}
	r.Discard()
}

// TestTenantShardRecode_ReleaseIsIdempotent proves the release lowers the
// migration guard and starts the quiesced shards exactly once: a second release
// starts nothing and reports success.
func TestTenantShardRecode_ReleaseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, dataRoot := migrateTestTenant(t, "vt-rel")
	f := migrateTestTenantShard(t, reg, hostReg, "vt-rel", dataRoot, "home", 2302)

	r := f.beginTenantShardRecode("vt-rel")
	if r.Empty() || len(r.StoreDirs()) != 2 {
		t.Fatalf("recode of a started shard with its own WAL: Empty=%v StoreDirs=%v, want two dirs", r.Empty(), r.StoreDirs())
	}
	if err := r.Quiesce(); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if _, err := f.Services(ctx, "vt-rel", "home"); !errors.Is(err, ErrShardMigrationInProgress) {
		t.Fatalf("Services while recoding = %v, want ErrShardMigrationInProgress", err)
	}
	for i := 0; i < 2; i++ {
		if err := r.Release(ctx, false); err != nil {
			t.Fatalf("Release #%d: %v", i+1, err)
		}
	}
	if _, err := f.Services(ctx, "vt-rel", "home"); err != nil {
		t.Fatalf("Services after release: %v", err)
	}
}
