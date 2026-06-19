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
	"errors"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-quicraft/pkg/logdb"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ErrNilSharedWAL is returned when a MultiplexLogDB is constructed without a
// shared (system-barrier) WAL. Every multiplex must own a shared WAL: it is the
// Route-A fallback for all shards that have not opted into a dedicated WAL.
var ErrNilSharedWAL = errors.New("database: multiplex log DB requires a non-nil shared WAL")

// ErrNilDedicatedWAL is returned when registering a nil dedicated WAL for a shard.
var ErrNilDedicatedWAL = errors.New("database: dedicated WAL must be non-nil")

// ErrShardAlreadyDedicated is returned when registering a dedicated WAL for a
// shard that already routes to a dedicated WAL.
var ErrShardAlreadyDedicated = errors.New("database: shard already routes to a dedicated WAL")

// DedicatedWALError identifies a failure operating on a per-shard dedicated WAL.
type DedicatedWALError struct {
	ShardID uint64
	Op      string
	Err     error
}

// Error implements the error interface.
func (e *DedicatedWALError) Error() string {
	return "database: dedicated WAL " + e.Op + " for shard " +
		strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *DedicatedWALError) Unwrap() error { return e.Err }

// walRoute pairs an underlying single-barrier LogDB with the barrier identity
// that encrypts its records. The barrier is retained so MinLiveEpochForBarrier
// can locate the owning WAL by identity and so BarrierForShard can hand the
// per-shard barrier to the host's snapshot encryptor.
type walRoute struct {
	db      logdb.LogDB
	barrier *seal.EpochBarrier
}

// MultiplexLogDB is a logdb.LogDB that fans Raft shards across several
// single-barrier WAL stores: one shared WAL encrypted under the system barrier
// (the default route for every shard) plus zero or more per-tenant dedicated
// WALs, each a single-barrier waldb.DB encrypted under that tenant's barrier.
//
// The 4d "Option C" adaptation lives here, NOT in waldb: a waldb.DB owns exactly
// one barrier, so per-tenant-KEY WAL isolation is achieved by routing each
// opted-in shard to its own dedicated waldb.DB while every other shard stays on
// the shared system-barrier WAL. Dispatch is by ShardID. The multiplex satisfies
// logdb.PerBarrierEpochAwareLogDB by delegating MinLiveEpochForBarrier to
// whichever underlying single-barrier WAL owns the requested barrier.
//
// When no dedicated WAL is registered the multiplex is a transparent pass-through
// to the shared WAL: every method delegates to it and BarrierForShard returns nil
// (so the host's snapshot encryptor falls back to the host-wide system barrier).
// This keeps the shared-WAL "Route A" path byte-identical to a bare shared WAL.
//
// MultiplexLogDB is safe for concurrent use. The control plane
// (Register/Unregister) takes a write lock; the data plane takes a read lock and
// snapshots the per-shard routing before delegating, so a dedicated WAL is never
// torn down mid-call.
type MultiplexLogDB struct {
	shared walRoute

	mu        sync.RWMutex
	dedicated map[uint64]walRoute

	closed atomic.Bool
}

// Compile-time interface checks.
var (
	_ logdb.LogDB                     = (*MultiplexLogDB)(nil)
	_ logdb.PerBarrierEpochAwareLogDB = (*MultiplexLogDB)(nil)
	_ logdb.BarrierShardEnumerator    = (*MultiplexLogDB)(nil)
)

// NewMultiplexLogDB builds a multiplex over the given shared WAL. sharedBarrier
// is the barrier (system barrier) that encrypts the shared WAL's records; it may
// be nil only when the shared WAL stores plaintext (testing). The shared WAL is
// the Route-A fallback for every shard that has not registered a dedicated WAL.
func NewMultiplexLogDB(shared logdb.LogDB, sharedBarrier *seal.EpochBarrier) (*MultiplexLogDB, error) {
	if shared == nil {
		return nil, ErrNilSharedWAL
	}
	return &MultiplexLogDB{
		shared:    walRoute{db: shared, barrier: sharedBarrier},
		dedicated: make(map[uint64]walRoute),
	}, nil
}

// Register routes shardID to a dedicated single-barrier WAL encrypted under
// barrier. It is called when an IsolationRaft group opts into a per-tenant WAL,
// before the shard is started on the host so the shard's first SaveState already
// lands in the dedicated WAL. Registering an already-dedicated shard is an error.
func (m *MultiplexLogDB) Register(shardID uint64, db logdb.LogDB, barrier *seal.EpochBarrier) error {
	if m.closed.Load() {
		return logdb.ErrClosed
	}
	if db == nil {
		return &DedicatedWALError{ShardID: shardID, Op: "register", Err: ErrNilDedicatedWAL}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.dedicated[shardID]; exists {
		return &DedicatedWALError{ShardID: shardID, Op: "register", Err: ErrShardAlreadyDedicated}
	}
	m.dedicated[shardID] = walRoute{db: db, barrier: barrier}
	return nil
}

// Unregister removes the dedicated WAL routing for shardID and returns the
// detached LogDB so the caller can close it (the tenant barrier purge performs
// the actual crypto-shred of the DEKs). It reports ok=false for a shard that was
// not routed to a dedicated WAL.
func (m *MultiplexLogDB) Unregister(shardID uint64) (logdb.LogDB, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.dedicated[shardID]
	if ok {
		delete(m.dedicated, shardID)
	}
	return r.db, ok
}

// IsDedicated reports whether shardID currently routes to a dedicated WAL.
func (m *MultiplexLogDB) IsDedicated(shardID uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.dedicated[shardID]
	return ok
}

// BarrierForShard returns the dedicated barrier that must encrypt shardID's
// snapshots, or nil when the shard routes to the shared WAL (the caller's
// resolver then falls back to the host-wide system barrier). It is the resolver
// installed via quicraft.WithShardBarriers.
func (m *MultiplexLogDB) BarrierForShard(shardID uint64) *seal.EpochBarrier {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if r, ok := m.dedicated[shardID]; ok {
		return r.barrier
	}
	return nil
}

// routeFor returns the WAL that owns shardID: the dedicated WAL when one is
// registered, otherwise the shared WAL.
func (m *MultiplexLogDB) routeFor(shardID uint64) logdb.LogDB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if r, ok := m.dedicated[shardID]; ok {
		return r.db
	}
	return m.shared.db
}

// Name identifies the implementation.
func (m *MultiplexLogDB) Name() string { return "multiplex" }

// SaveState persists a batch of Updates, splitting them by route: updates for
// shards on the shared WAL are written to it in one call, and updates for each
// dedicated shard are written to that shard's WAL.
//
// SaveState is NOT atomic across WALs (Invariant 4 of the Option C design): each
// underlying SaveState is durable on its own, but a crash between the shared
// write and a dedicated write (or between two dedicated writes) can leave the
// batch partially applied. This matches the per-shard durability the raft layer
// already tolerates — on recovery raft re-proposes any entry it cannot confirm
// was committed, so no committed entry is lost. Errors from every WAL are joined
// so a single failing dedicated WAL does not mask the others.
func (m *MultiplexLogDB) SaveState(updates []logdb.Update) error {
	if m.closed.Load() {
		return logdb.ErrClosed
	}
	if len(updates) == 0 {
		return nil
	}

	var sharedUpdates []logdb.Update
	byShard := make(map[uint64][]logdb.Update)

	m.mu.RLock()
	for _, u := range updates {
		if _, ok := m.dedicated[u.ShardID]; ok {
			byShard[u.ShardID] = append(byShard[u.ShardID], u)
		} else {
			sharedUpdates = append(sharedUpdates, u)
		}
	}
	dbs := make(map[uint64]logdb.LogDB, len(byShard))
	for sid := range byShard {
		dbs[sid] = m.dedicated[sid].db
	}
	shared := m.shared.db
	m.mu.RUnlock()

	var errs []error
	if len(sharedUpdates) > 0 {
		if err := shared.SaveState(sharedUpdates); err != nil {
			errs = append(errs, err)
		}
	}
	for sid, ups := range byShard {
		if err := dbs[sid].SaveState(ups); err != nil {
			errs = append(errs, &DedicatedWALError{ShardID: sid, Op: "save-state", Err: err})
		}
	}
	return errors.Join(errs...)
}

// IterateEntries dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) IterateEntries(entries []logdb.Entry, size uint64, shardID, replicaID, low, high, maxSize uint64) ([]logdb.Entry, uint64, error) {
	return m.routeFor(shardID).IterateEntries(entries, size, shardID, replicaID, low, high, maxSize)
}

// ReadState dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) ReadState(shardID, replicaID uint64) (logdb.State, logdb.Membership, error) {
	return m.routeFor(shardID).ReadState(shardID, replicaID)
}

// EntryRange dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) EntryRange(shardID, replicaID uint64) (uint64, uint64, error) {
	return m.routeFor(shardID).EntryRange(shardID, replicaID)
}

// RemoveEntriesTo dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) RemoveEntriesTo(shardID, replicaID, index uint64) error {
	return m.routeFor(shardID).RemoveEntriesTo(shardID, replicaID, index)
}

// Compact dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) Compact(shardID, replicaID uint64) error {
	return m.routeFor(shardID).Compact(shardID, replicaID)
}

// SaveSnapshot dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) SaveSnapshot(shardID, replicaID uint64, snapshot logdb.Snapshot) error {
	return m.routeFor(shardID).SaveSnapshot(shardID, replicaID, snapshot)
}

// GetSnapshot dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) GetSnapshot(shardID, replicaID uint64) (logdb.Snapshot, error) {
	return m.routeFor(shardID).GetSnapshot(shardID, replicaID)
}

// RemoveNodeData dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) RemoveNodeData(shardID, replicaID uint64) error {
	return m.routeFor(shardID).RemoveNodeData(shardID, replicaID)
}

// SaveBootstrap dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) SaveBootstrap(shardID, replicaID uint64, bootstrap logdb.Bootstrap) error {
	return m.routeFor(shardID).SaveBootstrap(shardID, replicaID, bootstrap)
}

// GetBootstrap dispatches to the WAL that owns the node's shard.
func (m *MultiplexLogDB) GetBootstrap(shardID, replicaID uint64) (logdb.Bootstrap, bool, error) {
	return m.routeFor(shardID).GetBootstrap(shardID, replicaID)
}

// ListNodeInfo aggregates node info across the shared WAL and every dedicated
// WAL so recovery sees every shard regardless of which WAL persisted it.
func (m *MultiplexLogDB) ListNodeInfo() ([]logdb.NodeInfo, error) {
	m.mu.RLock()
	dbs := make([]logdb.LogDB, 0, len(m.dedicated)+1)
	dbs = append(dbs, m.shared.db)
	for _, r := range m.dedicated {
		dbs = append(dbs, r.db)
	}
	m.mu.RUnlock()

	var out []logdb.NodeInfo
	var errs []error
	for _, db := range dbs {
		ni, err := db.ListNodeInfo()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, ni...)
	}
	return out, errors.Join(errs...)
}

// MinLiveEpochForBarrier returns the minimum live WAL epoch for the records
// encrypted under barrier, conforming to the fail-safe contract of
// logdb.PerBarrierEpochAwareLogDB.
//
// The multiplex finds EVERY underlying single-barrier WAL whose barrier identity
// equals barrier and returns the minimum of their live epochs. A single tenant
// barrier can encrypt more than one dedicated WAL (a tenant with several
// dedicated-WAL databases shares one barrier), so the floor must span all of
// them — using only the first owner would under-count and let a purge delete a
// DEK another owning WAL's records still need.
//
//   - known == false: NO underlying WAL owns barrier (the multiplex cannot
//     attribute it). The caller MUST fail safe and not purge. This is the
//     critical distinction the previous two-value signature could not express:
//     "no owner" looked identical to "owner with zero live records".
//   - known == true, epoch == 0: at least one WAL owns barrier and none holds a
//     live encrypted record under it (a true non-constraint).
//   - known == true, epoch > 0: the minimum live epoch across all owning WALs.
//
// An error from any owning WAL is propagated (fail-safe). If any owning WAL
// reports known == false for its own registered barrier, the result is
// indeterminate and the multiplex returns known == false.
func (m *MultiplexLogDB) MinLiveEpochForBarrier(barrier any) (epoch uint64, known bool, err error) {
	if m.closed.Load() {
		return 0, false, logdb.ErrClosed
	}
	if barrier == nil {
		return 0, false, nil
	}
	m.mu.RLock()
	owners := m.ownersForBarrierLocked(barrier)
	m.mu.RUnlock()
	if len(owners) == 0 {
		// No WAL holds records under this barrier: cannot attribute it.
		return 0, false, nil
	}

	var floor uint64
	for _, owner := range owners {
		ownerEpoch, ownerKnown, ownerErr := minLiveEpochForOwner(owner, barrier)
		if ownerErr != nil {
			return 0, false, ownerErr
		}
		if !ownerKnown {
			// An owning WAL could not attribute the barrier it is registered
			// under: the floor is indeterminate, fail safe.
			return 0, false, nil
		}
		if ownerEpoch > 0 && (floor == 0 || ownerEpoch < floor) {
			floor = ownerEpoch
		}
	}
	return floor, true, nil
}

// minLiveEpochForOwner queries a single owning WAL for its live epoch floor
// under barrier, normalizing the EpochAware and plain-LogDB cases to the
// fail-safe (epoch, known, err) shape. An owner found by identity is, by
// construction, known to own the barrier; a plain owner that exposes no epoch
// info contributes no constraint (epoch 0, known true).
func minLiveEpochForOwner(owner logdb.LogDB, barrier any) (uint64, bool, error) {
	if pb, ok := owner.(logdb.PerBarrierEpochAwareLogDB); ok {
		return pb.MinLiveEpochForBarrier(barrier)
	}
	if ea, ok := owner.(logdb.EpochAwareLogDB); ok {
		min, err := ea.MinLiveEpoch()
		if err != nil {
			return 0, false, err
		}
		return min, true, nil
	}
	return 0, true, nil
}

// ShardsForBarrier implements logdb.BarrierShardEnumerator. It reports the
// complete, fixed set of dedicated shards encrypted under barrier so the
// snapshot pool can verify its per-barrier snapshot scan covers every mapped
// shard. known is true ONLY for a registered dedicated barrier (its shard set
// is fixed and fully enumerable); it is false for the shared/system barrier —
// whose membership is open-ended (every non-dedicated shard) and therefore not
// enumerable — and for any barrier the multiplex does not own.
func (m *MultiplexLogDB) ShardsForBarrier(barrier any) ([]uint64, bool) {
	if m.closed.Load() || barrier == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	// The shared barrier's shard set is open-ended; not enumerable.
	if m.shared.barrier != nil && any(m.shared.barrier) == barrier {
		return nil, false
	}
	var shards []uint64
	for sid, r := range m.dedicated {
		if r.barrier != nil && any(r.barrier) == barrier {
			shards = append(shards, sid)
		}
	}
	if len(shards) == 0 {
		return nil, false
	}
	return shards, true
}

// ownersForBarrierLocked returns every underlying WAL whose barrier identity
// equals barrier (the shared WAL and/or any dedicated WALs). Callers hold m.mu
// (read).
func (m *MultiplexLogDB) ownersForBarrierLocked(barrier any) []logdb.LogDB {
	var owners []logdb.LogDB
	if m.shared.barrier != nil && any(m.shared.barrier) == barrier {
		owners = append(owners, m.shared.db)
	}
	for _, r := range m.dedicated {
		if r.barrier != nil && any(r.barrier) == barrier {
			owners = append(owners, r.db)
		}
	}
	return owners
}

// Close closes the shared WAL and every still-registered dedicated WAL. Errors
// are joined so a single failing WAL does not prevent the rest from closing.
func (m *MultiplexLogDB) Close() error {
	if m.closed.Swap(true) {
		return nil
	}
	m.mu.Lock()
	dedicated := m.dedicated
	m.dedicated = make(map[uint64]walRoute)
	m.mu.Unlock()

	var errs []error
	for sid, r := range dedicated {
		if err := r.db.Close(); err != nil {
			errs = append(errs, &DedicatedWALError{ShardID: sid, Op: "close", Err: err})
		}
	}
	if err := m.shared.db.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
