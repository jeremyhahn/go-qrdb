// Copyright (c) 2026 Jeremy Hahn
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

package user

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// PromoteToSystemUser idempotently marks an existing record System: true.
// It is the reconciliation path for a record that predates this invariant --
// a bootstrap admin created before the flag existed, or one whose flag did
// not persist for any other reason -- so pkg/firstboot can heal a
// pre-existing cluster on every start rather than only minting a fresh one.
//
// It enforces the SAME two invariants CreateUser does for a fresh System
// record: the target must be in the system tenant, and at most one System
// user may exist, recorded by the uniqueness sentinel. The sentinel and the
// record's flag are written as ONE guarded batch
// (kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged) guarded on the
// sentinel being absent AND the record being unchanged since it was read, so
// no interleaving can leave one written without the other: no crash leaves a
// sentinel naming a record that is not System, no concurrent promotion of a
// different record can also succeed, and no concurrent update of this record
// is overwritten. A store without that capability is refused with
// ErrStoreNotAtomic rather than promoted in two steps.
//
// A record already System: true is a success when the sentinel names it. If
// the sentinel is absent it is written, in the same kind of guarded batch;
// if it names a different record the store holds two System claims and the
// call refuses with SystemUserAlreadyExistsError rather than choosing one.
//
// A batch whose outcome is unknown returns CommitOutcomeUnknownError: the
// caller retries, and the retry answers from whatever actually committed.
func (s *kvUserStore) PromoteToSystemUser(ctx context.Context, id uint64) (*User, error) {
	if id == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}
	guarded, ok := s.store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		return nil, &StoreError{Op: "promote_system", Detail: "sentinel and record need one guarded batch", Err: ErrStoreNotAtomic}
	}
	idBytes := encodeID(id)
	pk := primaryKey(id)

	for attempt := 0; attempt < certBindingMutationAttempts; attempt++ {
		raw, err := s.store.Get(ctx, pk)
		if err != nil {
			return nil, &StoreError{Op: "promote_system", Detail: "primary lookup", Err: err}
		}
		if raw == nil {
			return nil, &UserNotFoundError{ID: id}
		}
		cur := &User{}
		if uerr := json.Unmarshal(raw, cur); uerr != nil {
			return nil, &StoreError{Op: "promote_system", Detail: "unmarshal", Err: uerr}
		}
		owner, err := s.handleOwner(ctx, keySystemSentinel)
		if err != nil {
			return nil, &StoreError{Op: "promote_system", Detail: "read system sentinel", Err: err}
		}
		if owner != 0 && owner != id {
			return nil, &SystemUserAlreadyExistsError{ExistingID: owner}
		}
		if cur.System && owner == id {
			return cur, nil
		}
		if cur.TenantID != tenant.SystemTenantID {
			return nil, &SystemTenantRequiredError{TenantID: cur.TenantID}
		}

		// Either the record needs the flag, the sentinel needs writing, or
		// both. Both are written together in every case, under both guards.
		next := cur
		if !cur.System {
			promoted := *cur
			promoted.System = true
			promoted.UpdatedAt = time.Now().UTC()
			next = &promoted
		}
		data, merr := json.Marshal(next)
		if merr != nil {
			return nil, &StoreError{Op: "promote_system", Detail: "marshal", Err: merr}
		}
		applied, err := guarded.PutAllIfAllUnchanged(ctx,
			[]kvstore.Guard{{Key: keySystemSentinel}, {Key: pk, Value: raw}},
			[]kvstore.KV{{Key: pk, Value: data}, {Key: keySystemSentinel, Value: idBytes}},
		)
		if err != nil {
			return nil, &CommitOutcomeUnknownError{ID: id, Err: err}
		}
		if applied {
			return next, nil
		}
		// A guard did not hold: the sentinel was claimed or the record
		// changed. The next pass re-reads both and answers from them.
	}
	return nil, &ConcurrentUpdateError{ID: id}
}

// existingSystemUserID best-effort resolves the id currently holding the
// system-uniqueness sentinel, purely to name it in
// SystemUserAlreadyExistsError. Returns zero when the holder cannot be read;
// the refusal itself never depends on this succeeding.
func (s *kvUserStore) existingSystemUserID(ctx context.Context) uint64 {
	raw, err := s.store.Get(ctx, keySystemSentinel)
	if err != nil || raw == nil {
		return 0
	}
	id, derr := decodeID(raw)
	if derr != nil {
		return 0
	}
	return id
}
