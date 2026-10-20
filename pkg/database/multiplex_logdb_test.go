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

package database

import (
	"context"
	"errors"
	"sync"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fakeLogDB is a recording logdb.LogDB used to assert routing and dispatch. It
// optionally satisfies logdb.PerBarrierEpochAwareLogDB / logdb.EpochAwareLogDB.
type fakeLogDB struct {
	mu sync.Mutex

	name string

	saved    [][]logdb.Update // one entry per SaveState call
	saveErr  error
	closeErr error
	closed   bool

	// shards records the shard IDs touched via per-shard dispatch.
	dispatched []uint64

	nodeInfo    []logdb.NodeInfo
	nodeInfoErr error

	// epoch returned by MinLiveEpochForBarrier/MinLiveEpoch (per matching barrier).
	epoch    uint64
	epochErr error
}

func (f *fakeLogDB) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}

func (f *fakeLogDB) SaveState(updates []logdb.Update) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]logdb.Update, len(updates))
	copy(cp, updates)
	f.saved = append(f.saved, cp)
	return f.saveErr
}

func (f *fakeLogDB) record(shardID uint64) {
	f.mu.Lock()
	f.dispatched = append(f.dispatched, shardID)
	f.mu.Unlock()
}

func (f *fakeLogDB) IterateEntries(entries []logdb.Entry, size, shardID, replicaID, low, high, maxSize uint64) ([]logdb.Entry, uint64, error) {
	f.record(shardID)
	return entries, size, nil
}

func (f *fakeLogDB) ReadState(shardID, replicaID uint64) (logdb.State, logdb.Membership, error) {
	f.record(shardID)
	return logdb.State{}, logdb.Membership{}, nil
}

func (f *fakeLogDB) EntryRange(shardID, replicaID uint64) (uint64, uint64, error) {
	f.record(shardID)
	return 0, 0, nil
}

func (f *fakeLogDB) RemoveEntriesTo(shardID, replicaID, index uint64) error {
	f.record(shardID)
	return nil
}

func (f *fakeLogDB) Compact(shardID, replicaID uint64) error {
	f.record(shardID)
	return nil
}

func (f *fakeLogDB) SaveSnapshot(shardID, replicaID uint64, snapshot logdb.Snapshot) error {
	f.record(shardID)
	return nil
}

func (f *fakeLogDB) GetSnapshot(shardID, replicaID uint64) (logdb.Snapshot, error) {
	f.record(shardID)
	return logdb.Snapshot{}, nil
}

func (f *fakeLogDB) RemoveNodeData(shardID, replicaID uint64) error {
	f.record(shardID)
	return nil
}

func (f *fakeLogDB) NodeRemoved(shardID, replicaID uint64) (bool, error) {
	f.record(shardID)
	return false, nil
}

func (f *fakeLogDB) SaveBootstrap(shardID, replicaID uint64, bootstrap logdb.Bootstrap) error {
	f.record(shardID)
	return nil
}

func (f *fakeLogDB) GetBootstrap(shardID, replicaID uint64) (logdb.Bootstrap, bool, error) {
	f.record(shardID)
	return logdb.Bootstrap{}, false, nil
}

func (f *fakeLogDB) ListNodeInfo() ([]logdb.NodeInfo, error) {
	return f.nodeInfo, f.nodeInfoErr
}

func (f *fakeLogDB) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}

func (f *fakeLogDB) MinLiveEpoch() (uint64, error) {
	return f.epoch, f.epochErr
}

// MinLiveEpochForBarrier conforms to the fail-safe 3-value contract. A fakeLogDB
// reached via the multiplex's owner lookup is, by construction, the owner of the
// queried barrier, so known is true whenever there is no error.
func (f *fakeLogDB) MinLiveEpochForBarrier(any) (uint64, bool, error) {
	if f.epochErr != nil {
		return 0, false, f.epochErr
	}
	return f.epoch, true, f.epochErr
}

// plainFake exposes neither epoch interface.
type plainFake struct {
	saved   [][]logdb.Update
	saveErr error
}

func (p *plainFake) Name() string { return "plain" }
func (p *plainFake) SaveState(updates []logdb.Update) error {
	p.saved = append(p.saved, updates)
	return p.saveErr
}
func (p *plainFake) IterateEntries(e []logdb.Entry, s, sh, r, l, h, m uint64) ([]logdb.Entry, uint64, error) {
	return e, s, nil
}
func (p *plainFake) ReadState(uint64, uint64) (logdb.State, logdb.Membership, error) {
	return logdb.State{}, logdb.Membership{}, nil
}
func (p *plainFake) EntryRange(uint64, uint64) (uint64, uint64, error)   { return 0, 0, nil }
func (p *plainFake) RemoveEntriesTo(uint64, uint64, uint64) error        { return nil }
func (p *plainFake) Compact(uint64, uint64) error                        { return nil }
func (p *plainFake) SaveSnapshot(uint64, uint64, logdb.Snapshot) error   { return nil }
func (p *plainFake) GetSnapshot(uint64, uint64) (logdb.Snapshot, error)  { return logdb.Snapshot{}, nil }
func (p *plainFake) RemoveNodeData(uint64, uint64) error                 { return nil }
func (p *plainFake) NodeRemoved(uint64, uint64) (bool, error)            { return false, nil }
func (p *plainFake) SaveBootstrap(uint64, uint64, logdb.Bootstrap) error { return nil }
func (p *plainFake) GetBootstrap(uint64, uint64) (logdb.Bootstrap, bool, error) {
	return logdb.Bootstrap{}, false, nil
}
func (p *plainFake) ListNodeInfo() ([]logdb.NodeInfo, error) { return nil, nil }
func (p *plainFake) Close() error                            { return nil }

// epochAwareFake implements logdb.EpochAwareLogDB but NOT
// logdb.PerBarrierEpochAwareLogDB, so the multiplex must fall back to
// MinLiveEpoch when this WAL owns the requested barrier.
type epochAwareFake struct {
	*plainFake
	epoch uint64
}

func (e *epochAwareFake) MinLiveEpoch() (uint64, error) { return e.epoch, nil }

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestDedicatedWALError_Error(t *testing.T) {
	e := &DedicatedWALError{ShardID: 42, Op: "save-state", Err: errors.New("disk full")}
	want := "database: dedicated WAL save-state for shard 42: disk full"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(e, e.Err) {
		t.Fatalf("Unwrap must expose the underlying error")
	}
}

func TestTenantWALUnavailableError_Error(t *testing.T) {
	e := &TenantWALUnavailableError{TenantID: "acme", ShardID: 42, Err: errors.New("disk full")}
	want := "database: dedicated WAL of tenant acme shard 42 is unavailable: disk full"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q; want %q", got, want)
	}
}

func TestTenantWALUnavailableError_Unwrap(t *testing.T) {
	var err error = &TenantWALUnavailableError{TenantID: "acme", ShardID: 42, Err: ErrDedicatedWALNotWired}
	if !errors.Is(err, ErrDedicatedWALNotWired) {
		t.Fatalf("errors.Is must reach the cause through Unwrap")
	}
	var target *TenantWALUnavailableError
	if !errors.As(err, &target) || target.ShardID != 42 {
		t.Fatalf("errors.As must find the TenantWALUnavailableError")
	}
}

func TestNewMultiplexLogDB_NilShared(t *testing.T) {
	if _, err := NewMultiplexLogDB(nil, nil); !errors.Is(err, ErrNilSharedWAL) {
		t.Fatalf("expected ErrNilSharedWAL, got %v", err)
	}
}

func TestNewMultiplexLogDB_OK(t *testing.T) {
	shared := &fakeLogDB{}
	m, err := NewMultiplexLogDB(shared, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name() != "multiplex" {
		t.Fatalf("Name = %q, want multiplex", m.Name())
	}
	if m.IsDedicated(1) {
		t.Fatalf("shard 1 should not be dedicated on a fresh multiplex")
	}
}

// ---------------------------------------------------------------------------
// Register / Unregister
// ---------------------------------------------------------------------------

func TestMultiplexRegister_NilDB(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, nil)
	err := m.Register(7, nil, nil)
	var dwe *DedicatedWALError
	if !errors.As(err, &dwe) || !errors.Is(err, ErrNilDedicatedWAL) {
		t.Fatalf("expected DedicatedWALError/ErrNilDedicatedWAL, got %v", err)
	}
	if dwe.ShardID != 7 || dwe.Op != "register" {
		t.Fatalf("unexpected error fields: %+v", dwe)
	}
}

func TestMultiplexRegister_Duplicate(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, nil)
	b := &seal.EpochBarrier{}
	if err := m.Register(7, &fakeLogDB{}, b); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := m.Register(7, &fakeLogDB{}, b)
	if !errors.Is(err, ErrShardAlreadyDedicated) {
		t.Fatalf("expected ErrShardAlreadyDedicated, got %v", err)
	}
}

func TestMultiplexRegister_OnClosed(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, nil)
	_ = m.Close()
	if err := m.Register(1, &fakeLogDB{}, nil); !errors.Is(err, logdb.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestMultiplexUnregister(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, nil)
	ded := &fakeLogDB{}
	if err := m.Register(9, ded, &seal.EpochBarrier{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !m.IsDedicated(9) {
		t.Fatalf("shard 9 should be dedicated after register")
	}
	got, ok := m.Unregister(9)
	if !ok || got != ded {
		t.Fatalf("Unregister returned (%v,%v), want (ded,true)", got, ok)
	}
	if m.IsDedicated(9) {
		t.Fatalf("shard 9 should not be dedicated after unregister")
	}
	if _, ok := m.Unregister(9); ok {
		t.Fatalf("second Unregister should report ok=false")
	}
}

// ---------------------------------------------------------------------------
// BarrierForShard
// ---------------------------------------------------------------------------

func TestMultiplexBarrierForShard(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, &seal.EpochBarrier{})
	if b := m.BarrierForShard(1); b != nil {
		t.Fatalf("undedicated shard must return nil barrier, got %v", b)
	}
	tb := &seal.EpochBarrier{}
	_ = m.Register(1, &fakeLogDB{}, tb)
	if b := m.BarrierForShard(1); b != tb {
		t.Fatalf("dedicated shard must return its barrier")
	}
	if b := m.BarrierForShard(2); b != nil {
		t.Fatalf("other shard must still return nil")
	}
}

// ---------------------------------------------------------------------------
// SaveState route split
// ---------------------------------------------------------------------------

func TestMultiplexSaveState_RouteSplit(t *testing.T) {
	shared := &fakeLogDB{}
	ded1 := &fakeLogDB{}
	ded2 := &fakeLogDB{}
	m, _ := NewMultiplexLogDB(shared, nil)
	_ = m.Register(11, ded1, &seal.EpochBarrier{})
	_ = m.Register(12, ded2, &seal.EpochBarrier{})

	updates := []logdb.Update{
		{ShardID: 1},  // shared
		{ShardID: 11}, // ded1
		{ShardID: 11}, // ded1 (same batch must group)
		{ShardID: 12}, // ded2
		{ShardID: 2},  // shared
	}
	if err := m.SaveState(updates); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	if len(shared.saved) != 1 || len(shared.saved[0]) != 2 {
		t.Fatalf("shared got %d calls / %v, want 1 call of 2 updates", len(shared.saved), shared.saved)
	}
	if len(ded1.saved) != 1 || len(ded1.saved[0]) != 2 {
		t.Fatalf("ded1 should get 1 call of 2 grouped updates, got %v", ded1.saved)
	}
	if len(ded2.saved) != 1 || len(ded2.saved[0]) != 1 {
		t.Fatalf("ded2 should get 1 call of 1 update, got %v", ded2.saved)
	}
}

func TestMultiplexSaveState_Empty(t *testing.T) {
	shared := &fakeLogDB{}
	m, _ := NewMultiplexLogDB(shared, nil)
	if err := m.SaveState(nil); err != nil {
		t.Fatalf("empty SaveState: %v", err)
	}
	if len(shared.saved) != 0 {
		t.Fatalf("empty SaveState must not call shared WAL")
	}
}

func TestMultiplexSaveState_OnClosed(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, nil)
	_ = m.Close()
	if err := m.SaveState([]logdb.Update{{ShardID: 1}}); !errors.Is(err, logdb.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

// TestMultiplexSaveState_NonAtomicPartialFailure documents and verifies that a
// failure on one dedicated WAL does not prevent writes to the others, and that
// every underlying error is surfaced (joined) — SaveState is not atomic across
// WALs (Invariant 4).
func TestMultiplexSaveState_NonAtomicPartialFailure(t *testing.T) {
	shared := &fakeLogDB{}
	good := &fakeLogDB{}
	bad := &fakeLogDB{saveErr: errors.New("disk full")}
	m, _ := NewMultiplexLogDB(shared, nil)
	_ = m.Register(11, good, &seal.EpochBarrier{})
	_ = m.Register(12, bad, &seal.EpochBarrier{})

	err := m.SaveState([]logdb.Update{
		{ShardID: 1},
		{ShardID: 11},
		{ShardID: 12},
	})
	if err == nil {
		t.Fatalf("expected a joined error from the failing WAL")
	}
	var dwe *DedicatedWALError
	if !errors.As(err, &dwe) || dwe.ShardID != 12 {
		t.Fatalf("expected DedicatedWALError for shard 12, got %v", err)
	}
	// The healthy shared + dedicated WALs were still written (non-atomic).
	if len(shared.saved) != 1 {
		t.Fatalf("shared WAL must still be written despite the failure")
	}
	if len(good.saved) != 1 {
		t.Fatalf("healthy dedicated WAL must still be written despite the failure")
	}
}

func TestMultiplexSaveState_SharedError(t *testing.T) {
	shared := &fakeLogDB{saveErr: errors.New("boom")}
	m, _ := NewMultiplexLogDB(shared, nil)
	if err := m.SaveState([]logdb.Update{{ShardID: 1}}); err == nil {
		t.Fatalf("expected shared SaveState error to propagate")
	}
}

// ---------------------------------------------------------------------------
// Per-shard dispatch
// ---------------------------------------------------------------------------

func TestMultiplexPerShardDispatch(t *testing.T) {
	shared := &fakeLogDB{}
	ded := &fakeLogDB{}
	m, _ := NewMultiplexLogDB(shared, nil)
	_ = m.Register(11, ded, &seal.EpochBarrier{})

	// Dedicated shard 11 → ded; shared shard 1 → shared.
	_, _, _ = m.IterateEntries(nil, 0, 11, 1, 0, 1, 1)
	_, _, _ = m.ReadState(11, 1)
	_, _, _ = m.EntryRange(11, 1)
	_ = m.RemoveEntriesTo(11, 1, 1)
	_ = m.Compact(11, 1)
	_ = m.SaveSnapshot(11, 1, logdb.Snapshot{})
	_, _ = m.GetSnapshot(11, 1)
	_ = m.RemoveNodeData(11, 1)
	_, _ = m.NodeRemoved(11, 1)
	_ = m.SaveBootstrap(11, 1, logdb.Bootstrap{})
	_, _, _ = m.GetBootstrap(11, 1)

	_, _, _ = m.IterateEntries(nil, 0, 1, 1, 0, 1, 1)
	_, _ = m.GetSnapshot(1, 1)

	ded.mu.Lock()
	dedHits := len(ded.dispatched)
	for _, s := range ded.dispatched {
		if s != 11 {
			t.Fatalf("dedicated WAL received non-11 shard %d", s)
		}
	}
	ded.mu.Unlock()
	if dedHits != 11 {
		t.Fatalf("dedicated WAL hit %d times, want 11", dedHits)
	}

	shared.mu.Lock()
	for _, s := range shared.dispatched {
		if s != 1 {
			t.Fatalf("shared WAL received non-1 shard %d", s)
		}
	}
	sharedHits := len(shared.dispatched)
	shared.mu.Unlock()
	if sharedHits != 2 {
		t.Fatalf("shared WAL hit %d times, want 2", sharedHits)
	}
}

// ---------------------------------------------------------------------------
// ListNodeInfo aggregation
// ---------------------------------------------------------------------------

func TestMultiplexListNodeInfo_Aggregate(t *testing.T) {
	shared := &fakeLogDB{nodeInfo: []logdb.NodeInfo{{ShardID: 1, ReplicaID: 1}}}
	ded := &fakeLogDB{nodeInfo: []logdb.NodeInfo{{ShardID: 11, ReplicaID: 1}}}
	m, _ := NewMultiplexLogDB(shared, nil)
	_ = m.Register(11, ded, &seal.EpochBarrier{})

	infos, err := m.ListNodeInfo()
	if err != nil {
		t.Fatalf("ListNodeInfo: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 node infos aggregated, got %d (%v)", len(infos), infos)
	}
}

func TestMultiplexListNodeInfo_Error(t *testing.T) {
	shared := &fakeLogDB{nodeInfoErr: errors.New("boom")}
	m, _ := NewMultiplexLogDB(shared, nil)
	if _, err := m.ListNodeInfo(); err == nil {
		t.Fatalf("expected aggregated error")
	}
}

// ---------------------------------------------------------------------------
// MinLiveEpochForBarrier delegation
// ---------------------------------------------------------------------------

func TestMultiplexMinLiveEpochForBarrier_Delegation(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	tenB := &seal.EpochBarrier{}
	shared := &fakeLogDB{epoch: 5}
	ded := &fakeLogDB{epoch: 9}
	m, _ := NewMultiplexLogDB(shared, sysB)
	_ = m.Register(11, ded, tenB)

	// System barrier → shared WAL's epoch, known=true.
	if got, known, err := m.MinLiveEpochForBarrier(sysB); err != nil || !known || got != 5 {
		t.Fatalf("MinLiveEpochForBarrier(sys) = (%d,%v,%v), want (5,true,nil)", got, known, err)
	}
	// Tenant barrier → dedicated WAL's epoch, known=true.
	if got, known, err := m.MinLiveEpochForBarrier(tenB); err != nil || !known || got != 9 {
		t.Fatalf("MinLiveEpochForBarrier(ten) = (%d,%v,%v), want (9,true,nil)", got, known, err)
	}
	// Unknown barrier → known=false (NOT owned). This is the HIGH-1 fail-safe
	// distinction: it must NOT look like a (0, owned) non-constraint.
	if got, known, err := m.MinLiveEpochForBarrier(&seal.EpochBarrier{}); err != nil || known || got != 0 {
		t.Fatalf("MinLiveEpochForBarrier(unknown) = (%d,%v,%v), want (0,false,nil)", got, known, err)
	}
	// nil barrier → known=false.
	if got, known, err := m.MinLiveEpochForBarrier(nil); err != nil || known || got != 0 {
		t.Fatalf("MinLiveEpochForBarrier(nil) = (%d,%v,%v), want (0,false,nil)", got, known, err)
	}
}

func TestMultiplexMinLiveEpochForBarrier_ErrorPropagates(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	shared := &fakeLogDB{epochErr: errors.New("cannot determine")}
	m, _ := NewMultiplexLogDB(shared, sysB)
	if _, known, err := m.MinLiveEpochForBarrier(sysB); err == nil || known {
		t.Fatalf("expected the owning WAL's error to propagate with known=false, got known=%v err=%v", known, err)
	}
}

func TestMultiplexMinLiveEpochForBarrier_EpochAwareFallback(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	shared := &epochAwareFake{plainFake: &plainFake{}, epoch: 7}
	m, _ := NewMultiplexLogDB(shared, sysB)
	if got, known, err := m.MinLiveEpochForBarrier(sysB); err != nil || !known || got != 7 {
		t.Fatalf("EpochAware fallback = (%d,%v,%v), want (7,true,nil)", got, known, err)
	}
}

func TestMultiplexMinLiveEpochForBarrier_PlainOwnerZero(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	m, _ := NewMultiplexLogDB(&plainFake{}, sysB)
	// A plain owner exposes no epoch info: it owns the barrier (known=true) and
	// imposes no WAL constraint (epoch 0).
	if got, known, err := m.MinLiveEpochForBarrier(sysB); err != nil || !known || got != 0 {
		t.Fatalf("plain owner = (%d,%v,%v), want (0,true,nil)", got, known, err)
	}
}

// TestMultiplexMinLiveEpochForBarrier_MultipleOwnersMin verifies that when one
// barrier encrypts MULTIPLE dedicated WALs (a tenant with several dedicated-WAL
// databases), the floor is the MINIMUM across all owning WALs — not just the
// first one found. Using a single owner would under-count and over-purge.
func TestMultiplexMinLiveEpochForBarrier_MultipleOwnersMin(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	tenB := &seal.EpochBarrier{} // one tenant barrier shared by two dedicated WALs
	shared := &fakeLogDB{epoch: 5}
	ded1 := &fakeLogDB{epoch: 9}
	ded2 := &fakeLogDB{epoch: 4} // older live epoch — must drive the floor
	m, _ := NewMultiplexLogDB(shared, sysB)
	_ = m.Register(11, ded1, tenB)
	_ = m.Register(12, ded2, tenB)

	if got, known, err := m.MinLiveEpochForBarrier(tenB); err != nil || !known || got != 4 {
		t.Fatalf("MinLiveEpochForBarrier(shared-by-two) = (%d,%v,%v), want (4,true,nil)", got, known, err)
	}
}

func TestMultiplexMinLiveEpochForBarrier_PlainOwnerZero_Dedicated(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	tenB := &seal.EpochBarrier{}
	m, _ := NewMultiplexLogDB(&fakeLogDB{epoch: 5}, sysB)
	_ = m.Register(11, &plainFake{}, tenB)
	// Dedicated owner is plain: owned (known=true), no constraint (epoch 0).
	if got, known, err := m.MinLiveEpochForBarrier(tenB); err != nil || !known || got != 0 {
		t.Fatalf("plain dedicated owner = (%d,%v,%v), want (0,true,nil)", got, known, err)
	}
}

func TestMultiplexMinLiveEpochForBarrier_OnClosed(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, &seal.EpochBarrier{})
	_ = m.Close()
	if _, known, err := m.MinLiveEpochForBarrier(&seal.EpochBarrier{}); !errors.Is(err, logdb.ErrClosed) || known {
		t.Fatalf("expected ErrClosed with known=false, got known=%v err=%v", known, err)
	}
}

// TestMultiplexShardsForBarrier verifies the BarrierShardEnumerator contract:
// the shared barrier is not enumerable (open-ended), an unknown barrier is not
// owned, and a dedicated barrier returns its complete fixed shard set.
func TestMultiplexShardsForBarrier(t *testing.T) {
	sysB := &seal.EpochBarrier{}
	tenB := &seal.EpochBarrier{}
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, sysB)
	_ = m.Register(11, &fakeLogDB{}, tenB)
	_ = m.Register(12, &fakeLogDB{}, tenB)

	// Shared barrier: open-ended, not enumerable.
	if shards, known := m.ShardsForBarrier(sysB); known {
		t.Fatalf("ShardsForBarrier(shared) = (%v, true), want (_, false)", shards)
	}
	// Unknown barrier: not owned.
	if shards, known := m.ShardsForBarrier(&seal.EpochBarrier{}); known {
		t.Fatalf("ShardsForBarrier(unknown) = (%v, true), want (_, false)", shards)
	}
	// nil barrier: not owned.
	if _, known := m.ShardsForBarrier(nil); known {
		t.Fatal("ShardsForBarrier(nil) known=true, want false")
	}
	// Dedicated barrier shared by two shards: complete fixed set {11,12}.
	shards, known := m.ShardsForBarrier(tenB)
	if !known {
		t.Fatal("ShardsForBarrier(dedicated) known=false, want true")
	}
	if len(shards) != 2 {
		t.Fatalf("ShardsForBarrier(dedicated) = %v, want 2 shards (11,12)", shards)
	}
	seen := map[uint64]bool{shards[0]: true, shards[1]: true}
	if !seen[11] || !seen[12] {
		t.Fatalf("ShardsForBarrier(dedicated) = %v, want {11,12}", shards)
	}

	// After unregistering one shard, the set shrinks to the remaining one.
	if _, ok := m.Unregister(11); !ok {
		t.Fatal("Unregister(11) ok=false")
	}
	shards, known = m.ShardsForBarrier(tenB)
	if !known || len(shards) != 1 || shards[0] != 12 {
		t.Fatalf("ShardsForBarrier after unregister = (%v,%v), want ([12],true)", shards, known)
	}

	// On a closed multiplex, the enumerator reports not-owned (fail safe).
	_ = m.Close()
	if _, known := m.ShardsForBarrier(tenB); known {
		t.Fatal("ShardsForBarrier on closed multiplex known=true, want false")
	}
}

// newInitializedEpochBarrier returns an initialized, unsealed
// *seal.EpochBarrier using the no-encryption strategy. waldb.Open refuses a
// sealed barrier — it cannot resolve a sealed barrier's context-binding
// capability — so tests that hand a real barrier to a real WAL must initialize
// it first.
func newInitializedEpochBarrier(t *testing.T) *seal.EpochBarrier {
	t.Helper()

	b, err := seal.NewEpochBarrier(seal.DefaultEpochBarrierConfig(1))
	if err != nil {
		t.Fatalf("new epoch barrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.NewNoneStrategy(), seal.Credentials{}); err != nil {
		t.Fatalf("initialize epoch barrier: %v", err)
	}
	return b
}

// TestMultiplexMinLiveEpochForBarrier_DynamicWALIdentity exercises the EXACT
// production identity path that a map-keyed mock cannot catch (HIGH-1): a real
// waldb.DB is dynamically registered under a real *seal.EpochBarrier, and the
// barrier handle is then routed back through the same conversions the running
// daemon uses — BarrierForShard returns *seal.EpochBarrier, the snapshot
// resolver boxes it as quicraft.Barrier, and the snapshot pool passes it to
// MinLiveEpochForBarrier as any. Identity must hold across every conversion so
// the owning WAL is found (known=true); an unregistered barrier must yield
// known=false; and after Unregister the same handle must again be unknown.
func TestMultiplexMinLiveEpochForBarrier_DynamicWALIdentity(t *testing.T) {
	const dedShard = uint64(7)

	// A real *seal.EpochBarrier (= go-quicraft's Barrier). No encrypted writes
	// occur, so MinLiveEpoch is 0; the test asserts the known bool and identity,
	// not a particular epoch value.
	//
	// It must be initialized rather than left as a zero value: waldb.Open
	// rejects a sealed barrier, because a sealed barrier cannot report whether
	// it authenticates caller context. The none strategy keeps the WAL in
	// plaintext, which is what this test wants — the assertion is about handle
	// identity across conversions, not about encryption.
	b := newInitializedEpochBarrier(t)

	dedDB, err := waldb.Open(t.TempDir(), waldb.WithBarrier(b))
	if err != nil {
		t.Fatalf("open dedicated waldb: %v", err)
	}
	m, err := NewMultiplexLogDB(&fakeLogDB{}, &seal.EpochBarrier{})
	if err != nil {
		t.Fatalf("new multiplex: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Before registration: the multiplex does not own b -> known=false.
	if _, known, err := m.MinLiveEpochForBarrier(b); err != nil || known {
		t.Fatalf("pre-register MinLiveEpochForBarrier = (known=%v, err=%v), want (false, nil)", known, err)
	}

	if err := m.Register(dedShard, dedDB, b); err != nil {
		t.Fatalf("register dedicated: %v", err)
	}

	// Route the barrier handle through the production conversions:
	// BarrierForShard (*seal.EpochBarrier) -> quicraft.Barrier -> any.
	routed := m.BarrierForShard(dedShard)
	if routed == nil {
		t.Fatal("BarrierForShard returned nil for the registered dedicated shard")
	}
	var asQuicraftBarrier quicraft.Barrier = routed // the WithShardBarriers conversion
	if _, known, err := m.MinLiveEpochForBarrier(asQuicraftBarrier); err != nil || !known {
		t.Fatalf("post-register MinLiveEpochForBarrier(routed) = (known=%v, err=%v), want (true, nil)", known, err)
	}
	// And directly with the original *seal.EpochBarrier handle.
	if _, known, err := m.MinLiveEpochForBarrier(b); err != nil || !known {
		t.Fatalf("post-register MinLiveEpochForBarrier(b) = (known=%v, err=%v), want (true, nil)", known, err)
	}

	// An unrelated barrier handle is never owned -> known=false (NOT a (0,true)
	// non-constraint that would let the caller over-purge).
	if _, known, _ := m.MinLiveEpochForBarrier(&seal.EpochBarrier{}); known {
		t.Fatal("MinLiveEpochForBarrier(other) known=true, want false")
	}

	// After Unregister the handle is unknown again, proving identity tracking is
	// tied to live registration.
	detached, ok := m.Unregister(dedShard)
	if !ok || detached == nil {
		t.Fatalf("Unregister(%d) = (%v, %v), want (non-nil, true)", dedShard, detached, ok)
	}
	t.Cleanup(func() { _ = detached.Close() })
	if _, known, err := m.MinLiveEpochForBarrier(b); err != nil || known {
		t.Fatalf("post-unregister MinLiveEpochForBarrier(b) = (known=%v, err=%v), want (false, nil)", known, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestMultiplexClose_ClosesAll(t *testing.T) {
	shared := &fakeLogDB{}
	ded := &fakeLogDB{}
	m, _ := NewMultiplexLogDB(shared, nil)
	_ = m.Register(11, ded, &seal.EpochBarrier{})
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !shared.closed || !ded.closed {
		t.Fatalf("Close must close shared (%v) and dedicated (%v) WALs", shared.closed, ded.closed)
	}
	// Idempotent.
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMultiplexClose_JoinsErrors(t *testing.T) {
	shared := &fakeLogDB{closeErr: errors.New("shared boom")}
	ded := &fakeLogDB{closeErr: errors.New("ded boom")}
	m, _ := NewMultiplexLogDB(shared, nil)
	_ = m.Register(11, ded, &seal.EpochBarrier{})
	err := m.Close()
	if err == nil {
		t.Fatalf("expected joined close error")
	}
	var dwe *DedicatedWALError
	if !errors.As(err, &dwe) {
		t.Fatalf("expected a DedicatedWALError in the joined error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Real waldb round-trip through the multiplex (real underlying LogDB).
// ---------------------------------------------------------------------------

func TestMultiplexRealWAL_RoundTrip(t *testing.T) {
	sharedDir := t.TempDir()
	dedDir := t.TempDir()

	sharedDB, err := waldb.Open(sharedDir)
	if err != nil {
		t.Fatalf("open shared waldb: %v", err)
	}
	dedDB, err := waldb.Open(dedDir)
	if err != nil {
		t.Fatalf("open dedicated waldb: %v", err)
	}

	m, err := NewMultiplexLogDB(sharedDB, nil)
	if err != nil {
		t.Fatalf("new multiplex: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	const dedShard = uint64(11)
	if err := m.Register(dedShard, dedDB, nil); err != nil {
		t.Fatalf("register dedicated: %v", err)
	}

	// Write entries to a shared shard and the dedicated shard via the mux.
	updates := []logdb.Update{
		{
			ShardID: 1, ReplicaID: 1,
			State:   logdb.State{Term: 1, Commit: 1},
			Entries: []logdb.Entry{{Index: 1, Term: 1, Cmd: []byte("shared-1")}},
		},
		{
			ShardID: dedShard, ReplicaID: 1,
			State:   logdb.State{Term: 2, Commit: 1},
			Entries: []logdb.Entry{{Index: 1, Term: 2, Cmd: []byte("ded-1")}},
		},
	}
	if err := m.SaveState(updates); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Shared shard entries land in the shared WAL only.
	got, _, err := m.IterateEntries(nil, 0, 1, 1, 1, 2, 1<<20)
	if err != nil {
		t.Fatalf("iterate shared: %v", err)
	}
	if len(got) != 1 || string(got[0].Cmd) != "shared-1" {
		t.Fatalf("shared entries = %v, want one shared-1", got)
	}
	// err is checked separately from the count: a failed IterateEntries
	// returns (nil, _, err), which would fail-closed on len()!=1 below but
	// send the reader after a WAL-routing bug for what is actually an I/O
	// failure on sharedDB (go-qrdb#254).
	directShared, _, err := sharedDB.IterateEntries(nil, 0, 1, 1, 1, 2, 1<<20)
	if err != nil {
		t.Fatalf("iterate shared WAL directly: %v", err)
	}
	if len(directShared) != 1 {
		t.Fatalf("shared WAL must hold the shared shard's entry directly")
	}
	// The shared WAL must NOT hold the dedicated shard's entries.
	//
	// The old `if dShared, _, _ := ...; len(dShared) != 0` folded a discarded
	// error into a length check: a failed call returns (nil, _, err), and
	// len(nil) != 0 is false, so it PASSED — reporting "not in the shared
	// WAL" — on a call that never actually read the shared WAL (go-qrdb#254,
	// the vacuous-assertion class #236 fixed elsewhere in this file).
	//
	// The fix is not a bare err!=nil Fatal, though: waldb.DB.IterateEntries's
	// OWN correct answer for "no index entry for this {shardID,replicaID}" is
	// logdb.ErrUnavailable (db.go, shard.indexes miss), not (nil, nil) — the
	// same idiom storage_durability_test.go uses upstream for ErrCompacted.
	// A bare err!=nil Fatal would misreport a genuinely-absent shard as a
	// WAL fault; errors.Is measures the one cause this assertion claims.
	dShared, _, err := sharedDB.IterateEntries(nil, 0, dedShard, 1, 1, 2, 1<<20)
	if !errors.Is(err, logdb.ErrUnavailable) {
		t.Fatalf("iterate dedicated shard's id in the shared WAL: got (%v, err=%v), want ErrUnavailable", dShared, err)
	}
	// The dedicated WAL holds the dedicated shard's entry.
	dDed, _, err := dedDB.IterateEntries(nil, 0, dedShard, 1, 1, 2, 1<<20)
	if err != nil {
		t.Fatalf("iterate dedicated WAL: %v", err)
	}
	if len(dDed) != 1 || string(dDed[0].Cmd) != "ded-1" {
		t.Fatalf("dedicated WAL entries = %v, want one ded-1", dDed)
	}

	// ListNodeInfo aggregates both WALs.
	infos, err := m.ListNodeInfo()
	if err != nil {
		t.Fatalf("ListNodeInfo: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 node infos (shared+dedicated), got %d", len(infos))
	}
}

// TestMultiplexConcurrentRegisterAndSave exercises the data plane concurrently
// with control-plane register/unregister to surface races under -race.
func TestMultiplexConcurrentRegisterAndSave(t *testing.T) {
	m, _ := NewMultiplexLogDB(&fakeLogDB{}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sid := uint64(100 + n)
			_ = m.Register(sid, &fakeLogDB{}, &seal.EpochBarrier{})
			_ = m.SaveState([]logdb.Update{{ShardID: sid}, {ShardID: 1}})
			_, _, _ = m.IterateEntries(nil, 0, sid, 1, 0, 1, 1)
			_ = m.BarrierForShard(sid)
			_, _ = m.Unregister(sid)
		}(i)
	}
	wg.Wait()
}
