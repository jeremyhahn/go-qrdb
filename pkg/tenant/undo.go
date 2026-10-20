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

package tenant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// tombstoneMinter builds the tombstone a teardown's fence writes:
// newTombstone for a deletion, newUndoneTombstone for an undone create.
type tombstoneMinter func(info *TenantInfo, now time.Time) *Tombstone

// CreateUndoer undoes a tenant create that did not complete. It is the undo
// half of an exclusive provisioning (pkg/bootstrap ProvisionTenant), and it
// is not a deletion: while the tenant-undo feature is active it leaves an
// undone tombstone, whose id a later create may reuse once every serving node
// has acknowledged its cleanup, where a deletion retires the id for good.
type CreateUndoer interface {
	UndoCreateTenant(ctx context.Context, tenantID string) error
}

// tenantUndoGate holds the tenant-undo activation check
// (featuregate.TenantUndo). A node that predates the feature never
// acknowledges an undo's cleanup, so a reuse of the undone id would wait on
// it forever: until every member can, an undo retires its id exactly as a
// deletion does.
type tenantUndoGate struct {
	tenantUndoRequired func() error

	// undoRoster lists the serving nodes an undone id's reuse waits on; see
	// WithUndoCleanupRoster.
	undoRoster UndoCleanupRoster
}

// undoActive reports whether an undo may leave an undone tombstone. A nil
// check leaves the feature off: the composer that wires the gate is also the
// one that wires the serving-node roster the acknowledgements are counted
// against, and an undo without that roster must not release an id.
func (g tenantUndoGate) undoActive() bool {
	return g.tenantUndoRequired != nil && g.tenantUndoRequired() == nil
}

// SetTenantUndoGate injects the tenant-undo activation check on a service
// built without options (NewKVTenantService); see WithTenantUndoGate.
func (g *tenantUndoGate) SetTenantUndoGate(required func() error) {
	g.tenantUndoRequired = required
}

// WithTenantUndoGate injects the tenant-undo activation check: while required
// returns an error (the composer's featuregate.NotActiveError), or when none
// is injected, UndoCreateTenant retires the id as DeleteTenant does.
func WithTenantUndoGate(required func() error) TenantServiceOption {
	return func(s *TenantService) { s.tenantUndoRequired = required }
}

// UndoCreateTenant undoes a tenant create: DeleteTenant's steps, with the
// fence writing an undone tombstone instead of a retired one. While the
// tenant-undo feature is not active it is DeleteTenant.
func (s *TenantService) UndoCreateTenant(ctx context.Context, tenantID string) error {
	if !s.undoActive() {
		return s.DeleteTenant(ctx, tenantID)
	}
	if err := s.deleteTenant(ctx, tenantID, newUndoneTombstone); err != nil {
		return err
	}
	// This node acknowledges its own cleanup now rather than at its next
	// reconcile pass. The undo has already committed: a failure here only
	// leaves the acknowledgement to that pass, so it is logged, not returned.
	if err := s.ReconcileUndone(ctx, tenantID); err != nil {
		s.logger.Warn("undone tenant cleanup left to the next reconcile",
			slog.String("tenant_id", tenantID), slog.String("error", err.Error()))
	}
	return nil
}

// UndoCreateTenant undoes a tenant create. See TenantService.UndoCreateTenant.
func (s *kvTenantService) UndoCreateTenant(ctx context.Context, tenantID string) error {
	if !s.undoActive() {
		return s.DeleteTenant(ctx, tenantID)
	}
	if err := s.deleteTenant(ctx, tenantID, newUndoneTombstone); err != nil {
		return err
	}
	// The teardown above already reclaimed what this service holds; see
	// TenantService.UndoCreateTenant for why a failure is logged.
	if err := s.ReconcileUndone(ctx, tenantID); err != nil {
		s.logger.Warn("undone tenant acknowledgement left to the next reconcile",
			slog.String("tenant_id", tenantID), slog.String("error", err.Error()))
	}
	return nil
}

// undoneAckPrefix is the keyspace of the per-node cleanup acknowledgements of
// undone creates: undoneAckPrefix + <id>/<nonce>/<node>, where <nonce> is the
// undone tombstone's Undo and <node> the acknowledging node's identity -- the
// same identity the UndoCleanupRoster lists.
const undoneAckPrefix = "__sys:tenants-undone-ack/"

// undoneAckScope is the prefix of every acknowledgement of one undo of id.
func undoneAckScope(id, nonce string) string { return undoneAckPrefix + id + "/" + nonce + "/" }

// undoneAckKey is node's acknowledgement of one undo of id.
func undoneAckKey(id, nonce, node string) string { return undoneAckScope(id, nonce) + node }

// undoneAck is the value of an acknowledgement. Its presence is what counts;
// the fields tell an operator who acknowledged and when.
type undoneAck struct {
	Node    string    `json:"node"`
	AckedAt time.Time `json:"acked_at"`
}

// UndoCleanupRoster lists, by node identity, the serving nodes whose cleanup
// acknowledgement a reuse of an undone id waits on.
type UndoCleanupRoster func(ctx context.Context) ([]string, error)

// UndoWiring is implemented by the tenant service NewKVTenantService returns,
// which takes its tenant-undo gate and cleanup roster after construction
// rather than as options.
type UndoWiring interface {
	SetTenantUndoGate(required func() error)
	SetUndoCleanupRoster(node string, roster UndoCleanupRoster)
}

var _ UndoWiring = (*kvTenantService)(nil)

// WithUndoCleanupRoster injects the serving-node roster an undone id's reuse
// is counted against. This node acknowledges under its WithNodeID identity,
// which must be one the roster lists.
func WithUndoCleanupRoster(roster UndoCleanupRoster) TenantServiceOption {
	return func(s *TenantService) { s.undoRoster = roster }
}

// SetUndoCleanupRoster injects, on a service built without options
// (NewKVTenantService), this node's identity and the serving-node roster; see
// WithUndoCleanupRoster.
func (s *kvTenantService) SetUndoCleanupRoster(node string, roster UndoCleanupRoster) {
	s.undoNode = node
	s.undoRoster = roster
}

// undoStore is the part of a tenant metadata store the undo records use.
type undoStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// ackUndone records node's cleanup acknowledgement of id's undo once this
// node holds nothing of it. It writes nothing when id's tombstone is absent or
// retired, when the teardown has not yet marked it TornDown (the shared
// keyspace may still hold the tenant's records), or when the acknowledgement
// is already there.
func ackUndone(ctx context.Context, store undoStore, id, node string) error {
	return ackUndoneUnder(ctx, store, id, node, "")
}

// ackUndoneUnder is ackUndone for a cleanup pass that began under the undo
// whose nonce is undo: it acknowledges that undo only, and refuses with
// *TenantUndoPendingError once the tombstone carries another, whose cleanup
// the pass did not do. An empty undo acknowledges whichever the tombstone
// carries.
func ackUndoneUnder(ctx context.Context, store undoStore, id, node, undo string) error {
	ts, ok, err := readTombstone(ctx, store.Get, id)
	if err != nil || !ok || !ts.Undone() || ts.Undo == "" || !ts.TornDown {
		return err
	}
	if undo != "" && ts.Undo != undo {
		return &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
	}
	if node == "" {
		return ErrUndoNodeUnnamed
	}
	key := undoneAckKey(id, ts.Undo, node)
	existing, err := store.Get(ctx, key)
	if err == nil && existing != nil {
		return nil
	}
	if err != nil && !isAbsent(err) {
		return &StoreError{Op: "undo-ack-get", Err: err}
	}
	data, err := json.Marshal(undoneAck{Node: node, AckedAt: time.Now().UTC()})
	if err != nil {
		return &StoreError{Op: "undo-ack-marshal", Err: err}
	}
	if err := store.Put(ctx, key, data); err != nil {
		return &StoreError{Op: "undo-ack-put", Err: err}
	}
	return nil
}

// ackUndoneCleanup is reconcileRetired's last step for an id this node holds
// nothing of: the acknowledgement of the undo the pass began under, written
// only while the pass is the id's sole holder, and reported as a reconcile
// failure so the next pass retries it.
func (s *TenantService) ackUndoneCleanup(ctx context.Context, p *undoPass) []*ResidueReconcileError {
	if err := p.acknowledge(ctx, s.store, s.nodeID); err != nil {
		return []*ResidueReconcileError{{TenantID: p.id, Op: "undo-ack", Err: err}}
	}
	return nil
}

// acknowledge writes node's acknowledgement of the undo the pass began
// under, only while the pass is the id's sole holder: see ackUndoneCleanup.
func (p *undoPass) acknowledge(ctx context.Context, store undoStore, node string) error {
	if p.undo == "" {
		// A retired id, or an undone one with no nonce: nothing acknowledges
		// it.
		return nil
	}
	if !p.beginAck() {
		// Another holder may still destroy state under the id: a rolled-back
		// create, a create that may still commit, or another pass. The last
		// of them runs the pass again once it is over.
		return &TenantUndoPendingError{TenantID: p.id, RetryAfter: UndoPendingRetryAfter}
	}
	defer p.endAck()
	return ackUndoneUnder(ctx, store, p.id, node, p.undo)
}

// beginCleanupPass begins reconcileRetired's pass over id. It reports done
// when the pass has nothing to do: the tombstone is gone, so the id was
// released and what remains under it is a later tenant's, or this node
// already acknowledged the undo the tombstone carries, after which a later
// create may already be provisioning under the id. The caller ends the
// returned pass, which is nil only with an error.
func (s *TenantService) beginCleanupPass(ctx context.Context, id string) (p *undoPass, done bool, err error) {
	return beginUndoPass(ctx, &s.undoAckHolds, s.store, s.nodeID, id)
}

// beginUndoPass is beginCleanupPass over holds and store for node, shared by
// every service that acknowledges undos.
func beginUndoPass(ctx context.Context, holds *undoAckHolds, store undoStore, node, id string) (p *undoPass, done bool, err error) {
	p, ok := holds.pass(id)
	if !ok {
		// Another pass is writing this node's acknowledgement.
		return nil, false, &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
	}
	ts, found, err := readTombstone(ctx, store.Get, id)
	if err != nil || !found {
		return p, true, err
	}
	if !ts.Undone() || ts.Undo == "" {
		return p, false, nil
	}
	p.undo = ts.Undo
	if node == "" {
		// The acknowledgement reports the unnamed node.
		return p, false, nil
	}
	acked, err := store.Get(ctx, undoneAckKey(id, ts.Undo, node))
	if err != nil && !isAbsent(err) {
		return p, true, &StoreError{Op: "undo-ack-get", Err: err}
	}
	return p, err == nil && acked != nil, nil
}

// endCleanupPass ends a pass begun by beginCleanupPass, running it again
// when another holder deferred its acknowledgement to it.
func (s *TenantService) endCleanupPass(ctx context.Context, p *undoPass) {
	s.reconcileDeferred(ctx, p.id, p.end())
}

// reconcileDeferred runs this node's cleanup pass over id again when deferred
// reports that an acknowledgement was withheld while the caller held the id.
func (s *TenantService) reconcileDeferred(ctx context.Context, id string, deferred bool) {
	rerunDeferred(ctx, s, s.logger, id, deferred)
}

// UndoReconciler is implemented by a tenant service that runs this node's
// cleanup of one undone id on demand, so a node learning of an undo
// acknowledges it at once rather than at its next reconcile pass.
type UndoReconciler interface {
	ReconcileUndone(ctx context.Context, tenantID string) error
}

var (
	_ UndoReconciler = (*TenantService)(nil)
	_ UndoReconciler = (*kvTenantService)(nil)
)

// ReconcileUndone runs this node's reconcile of tenantID when its tombstone is
// in the undone state: it reclaims what this node still holds of the id and,
// once nothing is left, records this node's acknowledgement. An absent or
// retired tombstone is left to ReconcileDeletedTenants.
func (s *TenantService) ReconcileUndone(ctx context.Context, tenantID string) error {
	ts, ok, err := readTombstone(ctx, s.store.Get, tenantID)
	if err != nil || !ok || !ts.Undone() {
		return err
	}
	guards, err := s.residueGuards(ctx)
	if err != nil {
		return err
	}
	report := &ReconcileReport{}
	failed, err := s.reconcileRetired(ctx, tenantID, guards, report)
	switch {
	case err != nil:
		return err
	case len(failed) > 0:
		return &ResidueReconcileFailuresError{Errors: failed}
	case slices.ContainsFunc(report.Skipped, func(sk SkippedPath) bool { return sk.Path != "" }):
		// A skipped directory withheld this node's acknowledgement. A skip
		// for the id's still-live record names no directory: the teardown
		// that holds the record has yet to finish, and its own pass
		// acknowledges.
		pending := &TenantUndoPendingError{TenantID: tenantID, RetryAfter: UndoPendingRetryAfter}
		if s.nodeID != "" {
			pending.Pending = []string{s.nodeID}
		}
		return pending
	}
	return nil
}

// ReconcileUndone records this node's acknowledgement of tenantID's undo:
// the teardown already reclaimed everything this service holds of it. An
// absent or retired tombstone is left alone. It is a cleanup pass as
// TenantService's is, so it never acknowledges while a teardown of the id
// is in flight on this node; the last one to end runs it again.
func (s *kvTenantService) ReconcileUndone(ctx context.Context, tenantID string) error {
	p, done, err := beginUndoPass(ctx, &s.undoAckHolds, s.store, s.undoNode, tenantID)
	if p == nil {
		return err
	}
	defer func() { rerunDeferred(ctx, s, s.logger, tenantID, p.end()) }()
	if done || err != nil {
		return err
	}
	return p.acknowledge(ctx, s.store, s.undoNode)
}

// undoReleaser releases an undone id to a create that met its tombstone. It
// reports released=true once the tombstone is gone, false when id's tombstone
// is absent or retired, and *TenantUndoPendingError while a serving node has
// not acknowledged the undo.
type undoReleaser func(ctx context.Context, id string) (released bool, err error)

// undoRelease is the releaser commitCreate tries, or nil while the tenant-undo
// feature is not active -- every tombstone then retires its id.
func (g tenantUndoGate) undoRelease(store undoStore) undoReleaser {
	if !g.undoActive() {
		return nil
	}
	return func(ctx context.Context, id string) (bool, error) {
		return releaseUndone(ctx, store, g.undoRoster, id)
	}
}

// compareAndDeleter is the conditional delete that releases a tombstone only
// as it was read.
type compareAndDeleter interface {
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
}

// compareAndSwapper is the conditional rewrite markTornDown needs, so that
// marking a teardown complete never resurrects a tombstone a release has
// removed or a later undo has replaced.
type compareAndSwapper interface {
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

// markTornDown is the last step of an undone tenant's teardown: it sets
// TornDown on the id's undone tombstone, which is what lets a node
// acknowledge the undo. A retired, absent or already-marked tombstone is left
// alone. A tombstone rewritten since it was read belongs to a release or a
// later undo, whose own teardown marks it, so losing the swap is success.
func markTornDown(ctx context.Context, store undoStore, id string) error {
	key := tombstoneKey(id)
	data, err := store.Get(ctx, key)
	if err != nil && !isAbsent(err) {
		return &StoreError{Op: "tombstone-get", Err: err}
	}
	if err != nil || data == nil {
		return nil
	}
	var ts Tombstone
	if json.Unmarshal(data, &ts) != nil || !ts.Undone() || ts.TornDown {
		return nil
	}
	cas, ok := store.(compareAndSwapper)
	if !ok {
		return ErrTenantStoreNotAtomic
	}
	ts.TornDown = true
	next, err := json.Marshal(&ts)
	if err != nil {
		return &StoreError{Op: "marshal-tombstone", Err: err}
	}
	if _, err := cas.CompareAndSwap(ctx, key, data, next); err != nil {
		return &StoreError{Op: "tombstone-torn-down", Err: err}
	}
	return nil
}

// putIfAbsenter is the store's create-only write.
type putIfAbsenter interface {
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
}

// putTombstone writes the fence's tombstone for id. A retirement is written
// unconditionally: a deletion always wins, over no tombstone, an undone one
// or an earlier retirement. An undone tombstone never replaces a retirement:
// it is created only where no tombstone is, or swapped only for the undone
// one it read, so an undo racing a deletion of the same id cannot downgrade
// the deletion to a reusable id. The undo that loses gets the retirement's
// *TenantDeletedError.
func putTombstone(ctx context.Context, store undoStore, id string, ts *Tombstone) error {
	data, err := json.Marshal(ts)
	if err != nil {
		return &StoreError{Op: "marshal-tombstone", Err: err}
	}
	key := tombstoneKey(id)
	if !ts.Undone() {
		if err := store.Put(ctx, key, data); err != nil {
			return &StoreError{Op: "put-tombstone", Err: err}
		}
		return nil
	}
	for attempt := 0; attempt < maxTeardownFenceAttempts; attempt++ {
		prev, err := store.Get(ctx, key)
		if err != nil && !isAbsent(err) {
			return &StoreError{Op: "tombstone-get", Err: err}
		}
		var written bool
		if err != nil || prev == nil {
			pia, ok := store.(putIfAbsenter)
			if !ok {
				return ErrTenantStoreNotAtomic
			}
			if written, err = pia.PutIfAbsent(ctx, key, data); err != nil {
				return &StoreError{Op: "put-tombstone", Err: err}
			}
		} else {
			var held Tombstone
			if json.Unmarshal(prev, &held) != nil || !held.Undone() {
				// A tombstone that will not decode is a retirement, as
				// readTombstone's callers read it.
				return &TenantDeletedError{TenantID: id, DeletedAt: held.DeletedAt}
			}
			cas, ok := store.(compareAndSwapper)
			if !ok {
				return ErrTenantStoreNotAtomic
			}
			if written, err = cas.CompareAndSwap(ctx, key, prev, data); err != nil {
				return &StoreError{Op: "put-tombstone", Err: err}
			}
		}
		if written {
			return nil
		}
	}
	return &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
}

// releaseUndone deletes id's undone tombstone once every roster member has
// acknowledged the undo, then that undo's acknowledgements. The delete is
// conditioned on the exact bytes read, so a tombstone rewritten in between --
// a later undo, a deletion -- is never released on the strength of the
// acknowledgements of the one read. With no roster nothing is released.
func releaseUndone(ctx context.Context, store undoStore, roster UndoCleanupRoster, id string) (bool, error) {
	key := tombstoneKey(id)
	data, err := store.Get(ctx, key)
	if err != nil && !isAbsent(err) {
		return false, &StoreError{Op: "tombstone-get", Err: err}
	}
	if err != nil || data == nil {
		return false, nil
	}
	var ts Tombstone
	if json.Unmarshal(data, &ts) != nil || !ts.Undone() || ts.Undo == "" {
		return false, nil
	}
	if roster == nil {
		return false, &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
	}
	members, err := roster(ctx)
	if err != nil {
		return false, &StoreError{Op: "undo-roster", Err: err}
	}
	scope := undoneAckScope(id, ts.Undo)
	acks, err := store.List(ctx, scope)
	if err != nil {
		return false, &StoreError{Op: "undo-ack-list", Err: err}
	}
	acked := make(map[string]bool, len(acks))
	for _, k := range acks {
		acked[strings.TrimPrefix(k, scope)] = true
	}
	var pending []string
	for _, m := range members {
		if !acked[m] {
			pending = append(pending, m)
		}
	}
	if len(members) == 0 || len(pending) > 0 {
		return false, &TenantUndoPendingError{TenantID: id, Pending: pending, RetryAfter: UndoPendingRetryAfter}
	}
	cad, ok := store.(compareAndDeleter)
	if !ok {
		return false, ErrTenantStoreNotAtomic
	}
	deleted, err := cad.CompareAndDelete(ctx, key, data)
	if err != nil {
		return false, &StoreError{Op: "tombstone-release", Err: err}
	}
	if !deleted {
		// Rewritten since it was read: the caller retries and reads it anew.
		return false, &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
	}
	for _, k := range acks {
		if err := store.Delete(ctx, k); err != nil && !isAbsent(err) {
			return false, &StoreError{Op: "undo-ack-delete", Err: err}
		}
	}
	return true, nil
}

// undoRefusal is the releaser for a create's commit: it never releases an
// id, and reports an undone tombstone found there as pending. That tombstone
// was written while the create provisioned, so what the create provisioned
// may be what the undo's teardown destroyed; the create rolls back, and its
// retry releases the id before it provisions anew.
func (g tenantUndoGate) undoRefusal(store undoStore) undoReleaser {
	if !g.undoActive() {
		return nil
	}
	return func(ctx context.Context, id string) (bool, error) {
		ts, ok, err := readTombstone(ctx, store.Get, id)
		if err != nil || !ok || !ts.Undone() {
			return false, err
		}
		return false, &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
	}
}

// maxTeardownFenceAttempts bounds fenceTeardown's re-reads of a tombstone
// other writers keep rewriting.
const maxTeardownFenceAttempts = 8

// fenceTeardown decides whether a destruction of state under id may go
// ahead, and makes it safe before it reports true. Its callers are a create
// that lost its commit to id's tombstone and is destroying what it
// provisioned (rollbackHeld), and every DeleteTenant or UndoCreateTenant
// teardown, retries included (guardTeardown). The catalog Assignment, barrier
// posture records and keyspace are keyed by id alone, so they are the
// caller's to destroy only while no later create can hold them, which is
// only while a tombstone still fences the id:
//
//   - no tombstone: the id was released, and a later create may already hold
//     the records. false.
//   - retired, unreadable, or undone with no nonce: never released. true.
//   - undone, not yet torn down: no node can have acknowledged it, and the
//     caller holds this node's acknowledgement (undoAckHolds). true.
//   - undone and torn down: acknowledgements may already release it, so it
//     is swapped for the same tombstone under a fresh nonce, which voids them
//     all; then as above. A lost swap is read again.
//
// The caller must hold id in undoAckHolds before calling.
func fenceTeardown(ctx context.Context, store undoStore, id string) (bool, error) {
	key := tombstoneKey(id)
	for attempt := 0; attempt < maxTeardownFenceAttempts; attempt++ {
		data, err := store.Get(ctx, key)
		if err != nil && !isAbsent(err) {
			return false, &StoreError{Op: "tombstone-get", Err: err}
		}
		if err != nil || data == nil {
			return false, nil
		}
		var ts Tombstone
		if json.Unmarshal(data, &ts) != nil || !ts.Undone() || !ts.TornDown || ts.Undo == "" {
			return true, nil
		}
		cas, ok := store.(compareAndSwapper)
		if !ok {
			return false, ErrTenantStoreNotAtomic
		}
		voided := ts.Undo
		ts.Undo = rand.Text()
		next, err := json.Marshal(&ts)
		if err != nil {
			return false, &StoreError{Op: "marshal-tombstone", Err: err}
		}
		swapped, err := cas.CompareAndSwap(ctx, key, data, next)
		if err != nil {
			return false, &StoreError{Op: "tombstone-renonce", Err: err}
		}
		if !swapped {
			continue
		}
		// Acknowledgements of the voided nonce count for nothing now, and the
		// release that would have deleted them never will. Best effort: one
		// left behind is inert.
		if acks, err := store.List(ctx, undoneAckScope(id, voided)); err == nil {
			for _, k := range acks {
				_ = store.Delete(ctx, k)
			}
		}
		return true, nil
	}
	return false, &TenantUndoPendingError{TenantID: id, RetryAfter: UndoPendingRetryAfter}
}

// holdTeardown fences one DeleteTenant or UndoCreateTenant teardown of id on
// this node with the acknowledgement fence a rolled-back create holds (see
// rollbackHeld), whichever service runs it. It takes id's hold in holds at
// once, and the returned end releases it. The caller defers end over the
// whole teardown, and calls guardTeardown before its first destructive step.
//
// Without the hold, an overlapping teardown -- a retried delete, or an undo
// racing another -- could still be reclaiming when the other one marks the
// id torn down, and this node would acknowledge a cleanup still in flight.
// With it, this node's acknowledgement waits for the slower teardown, so the
// id is never released, and never created again, until both are over. An
// acknowledgement a cleanup pass withheld for the hold is re-run by end
// through r.
func holdTeardown(ctx context.Context, holds *undoAckHolds, r UndoReconciler, logger *slog.Logger, id string) (end func()) {
	release := holds.hold(id)
	return func() { rerunDeferred(ctx, r, logger, id, release()) }
}

// guardTeardown is fenceTeardown for a teardown under holdTeardown's hold,
// run just before its first destructive step. It voids every
// acknowledgement of id's undo given before the hold, which may describe a
// cleanup this teardown is about to repeat, and reports false, with nothing
// to destroy, once the id has been released: every node then acknowledged a
// completed teardown, and what is keyed by id may already be the next
// tenant's. A failure is a *TeardownError, as the teardown's own are.
func guardTeardown(ctx context.Context, store undoStore, logger *slog.Logger, id string) (destroy bool, err error) {
	fenced, err := fenceTeardown(ctx, store, id)
	if err != nil {
		return false, &TeardownError{TenantID: id, Err: err}
	}
	if !fenced {
		logger.Info("tenant teardown superseded: the undone id was released once every node acknowledged its cleanup",
			slog.String("tenant_id", id))
	}
	return fenced, nil
}

// rerunDeferred runs r's cleanup pass over id again when a hold's release
// reported that an acknowledgement was deferred to it. The pass already
// committed nothing it could lose, so a failure only leaves the
// acknowledgement to the next reconcile and is logged.
func rerunDeferred(ctx context.Context, r UndoReconciler, logger *slog.Logger, id string, deferred bool) {
	if !deferred {
		return
	}
	if err := r.ReconcileUndone(ctx, id); err != nil {
		logger.Warn("undone tenant acknowledgement left to the next reconcile",
			slog.String("tenant_id", id), slog.String("error", err.Error()))
	}
}

// undoAckHolds withholds this node's acknowledgement of an undone id while
// anything on this node may still destroy state under that id: a rolled-back
// create destroying what it provisioned, and every cleanup pass over the id.
// An undo is released only once every node acknowledged it, so a held id
// cannot be released, and cannot be created again, until that destruction is
// over.
//
// A cleanup pass acknowledges only as the id's sole holder, so of two
// overlapping passes the first to finish leaves the acknowledgement to the
// other, never acknowledging while the other may still destroy. No pass
// begins while another is writing the acknowledgement: what it would destroy
// may belong to the id's next tenant once that write lands.
//
// Lock-free: each id maps to a count of its holders, with holdAcking and
// holdDeferred beside it. A count of -1 marks an entry its last release is
// removing, which a new hold waits out rather than counting itself into an
// entry no longer in the map.
type undoAckHolds struct{ m sync.Map }

const (
	// holdAcking marks the one cleanup pass writing id's acknowledgement.
	holdAcking int32 = 1 << 30
	// holdDeferred records an acknowledgement withheld because another
	// holder was in flight. The last holder to release reports it, and runs
	// the cleanup pass again, so the acknowledgement is never lost to a
	// withholding no holder remains to undo.
	holdDeferred int32 = 1 << 29
	// holdHolders masks the number of holders.
	holdHolders = holdDeferred - 1
)

// acquire adds a holder for id. A cleanup pass (pass) is refused while
// another pass writes id's acknowledgement.
func (h *undoAckHolds) acquire(id string, pass bool) (*atomic.Int32, bool) {
	for {
		v, _ := h.m.LoadOrStore(id, new(atomic.Int32))
		n, ok := v.(*atomic.Int32)
		if !ok {
			// Only counts are stored; anything else is not a hold, so it
			// is dropped and the hold retried.
			h.m.CompareAndDelete(id, v)
			continue
		}
		c := n.Load()
		if c >= 0 && pass && c&holdAcking != 0 {
			return nil, false
		}
		if c >= 0 && n.CompareAndSwap(c, c+1) {
			return n, true
		}
		runtime.Gosched()
	}
}

// drop removes a holder of id, reporting whether it was the last while an
// acknowledgement was deferred.
func (h *undoAckHolds) drop(id string, n *atomic.Int32) (deferred bool) {
	for {
		c := n.Load()
		next := c - 1
		deferred = next&holdHolders == 0 && next&holdDeferred != 0
		if deferred {
			next &^= holdDeferred
		}
		if n.CompareAndSwap(c, next) {
			if next == 0 && n.CompareAndSwap(0, -1) {
				h.m.CompareAndDelete(id, n)
			}
			return deferred
		}
	}
}

// hold withholds id's acknowledgement until the returned release runs.
// release reports whether a cleanup pass deferred its acknowledgement to it,
// in which case the caller runs the pass again.
func (h *undoAckHolds) hold(id string) (release func() (deferred bool)) {
	n, _ := h.acquire(id, false)
	return func() bool { return h.drop(id, n) }
}

// pass begins a cleanup pass over id. It reports false while another pass
// writes id's acknowledgement.
func (h *undoAckHolds) pass(id string) (*undoPass, bool) {
	n, ok := h.acquire(id, true)
	if !ok {
		return nil, false
	}
	return &undoPass{holds: h, id: id, n: n}, true
}

// held reports whether id's acknowledgement is withheld.
func (h *undoAckHolds) held(id string) bool {
	v, _ := h.m.Load(id)
	n, ok := v.(*atomic.Int32)
	return ok && n.Load() > 0
}

// undoPass is one cleanup pass over an id on this node: its hold on the id's
// acknowledgement, and the undo it began under.
type undoPass struct {
	holds *undoAckHolds
	id    string
	n     *atomic.Int32

	// undo is the nonce of the undo the pass began under, empty for a
	// retired id. The pass acknowledges that undo or none.
	undo string
}

// end releases the pass's hold, reporting whether an acknowledgement was
// deferred to it.
func (p *undoPass) end() bool { return p.holds.drop(p.id, p.n) }

// beginAck makes the pass the one writing the id's acknowledgement. It fails
// while any other holder is in flight, and defers the acknowledgement to the
// last of them.
func (p *undoPass) beginAck() bool {
	for {
		c := p.n.Load()
		if c&holdHolders != 1 {
			p.n.Or(holdDeferred)
			return false
		}
		if p.n.CompareAndSwap(c, c|holdAcking) {
			return true
		}
	}
}

// endAck ends beginAck.
func (p *undoPass) endAck() { p.n.Add(-holdAcking) }
