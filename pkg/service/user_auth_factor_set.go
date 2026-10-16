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

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Factor-set record
// ---------------------------------------------------------------------------
//
// A conditional add ("only if the user holds no active factor of this type",
// "only if the factor set is still at version N") has to be decided by
// replicated state, not by a List the caller -- or this service -- made a
// moment earlier: two callers that both list, both see nothing, and both
// write have both "won". The decision therefore rides on ONE guarded write:
//
//	__sys:auth/factorsets/{userID} -> {"version": N, "active": {factorID: type}}
//
// Every membership change -- Add, Remove, Rotate -- commits through it with
// PutIfAbsent (no record yet) or CompareAndSwap on the exact bytes the change
// was decided against. Of N callers racing the same condition, one swap lands;
// every other caller's swap is refused, it re-reads the record, finds the
// winner's factor in it, and is refused with *auth.FactorConditionFailedError.
// No new Raft opcode is involved: the guard is the store's own
// compare-and-swap, evaluated in the state machine on a replicated store.
//
// The record is a commit token, not a second copy of the user index. What it
// counts as active is re-derived at every commit from the record's ids AND the
// user index, each kept only while its primary record exists and is not
// revoked. That makes it self-healing across the windows between the guarded
// write and the plain writes around it: a factor written by a node that
// predates the record is still counted, a factor whose revocation landed
// without its commit is no longer counted, and a user who never had a record
// starts from the index at version 0.
//
// The primary record is written BEFORE the commit, so a refused add leaves
// nothing a login or a List can reach; its primary is released with
// CompareAndDelete on the exact bytes it wrote. On a store with multi-guarded
// batches -- every production store -- the indexes commit in the same batch as
// the factor set (commitEnrollment), so a concurrent hard Delete cannot land
// between them, and Remove and Rotate write their records in the same batch
// as the set (commitRemoval, commitRotation), so a failed write leaves
// neither. On a store with only a single-key guard the plain writes surround
// the commit, and such a store refuses a hard Delete.
const userFactorSetPrefix = "__sys:auth/factorsets/"

// factorSetCommitAttempts bounds the commit loop. Every refused swap means
// another change to the same user's factor set committed in between, so
// reaching the bound takes that many interleaved commits to one user.
const factorSetCommitAttempts = 64

// ErrUserAuthFactorAtomicStoreRequired is returned by a conditional factor add
// when the configured store cannot perform a guarded write. Production stores are atomic (a tenant.StoreResolver's stores
// implement kvstore.AtomicKVStore); this signals a composition that is not,
// and the call is refused rather than decided by a racy read.
var ErrUserAuthFactorAtomicStoreRequired = errors.New("service: kv store does not support atomic compare-and-swap; a conditional factor add cannot be decided")

// ErrUserAuthFactorBatchStoreRequired is returned by a hard factor Delete when
// the configured store cannot apply a multi-guarded batch. The record and its
// indexes must leave together; deleting them one at a time could strand an
// index that keeps the credential reserved, so the call is refused before
// anything is written.
var ErrUserAuthFactorBatchStoreRequired = errors.New("service: kv store does not support multi-guarded batches; a factor cannot be hard-deleted")

// ErrFactorSetContended is the sentinel *FactorSetContendedError unwraps to.
var ErrFactorSetContended = errors.New("service: factor set commit contended")

// FactorSetContendedError is returned when a change to a user's factor set
// was refused factorSetCommitAttempts times in a row because other changes
// kept committing first. Nothing was enrolled; the call may be retried.
type FactorSetContendedError struct {
	// UserID is the user whose factor set was contended.
	UserID uint64
	// Attempts is how many guarded writes were refused.
	Attempts int
}

// Error implements error.
func (e *FactorSetContendedError) Error() string {
	return "service: factor set of user " + strconv.FormatUint(e.UserID, 10) +
		" contended: " + strconv.Itoa(e.Attempts) + " guarded writes refused"
}

// Unwrap returns the sentinel error so errors.Is succeeds.
func (e *FactorSetContendedError) Unwrap() error { return ErrFactorSetContended }

// FactorSetDecodeError is returned when a stored record the factor-set
// decision depends on cannot be decoded. The decision fails closed: a record
// whose type cannot be read cannot be shown not to satisfy a condition.
type FactorSetDecodeError struct {
	// Key is the record that failed to decode.
	Key string
	// Err is the decode failure.
	Err error
}

// Error implements error.
func (e *FactorSetDecodeError) Error() string {
	return "service: decode " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the decode failure.
func (e *FactorSetDecodeError) Unwrap() error { return e.Err }

// factorSetStore is the guarded surface the factor-set record needs beyond
// AuthFactorKV. kvstore.AtomicKVStore provides it.
type factorSetStore interface {
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)
}

// factorSet is the stored factor-set record.
type factorSet struct {
	// Version moves on every committed membership change.
	Version uint64 `json:"version"`
	// Active maps each active factor id to its type.
	Active map[string]auth.AuthFactorType `json:"active,omitempty"`
}

// factorSetKey is the factor-set record key of userID.
func factorSetKey(userID uint64) string {
	return userFactorSetPrefix + strconv.FormatUint(userID, 10)
}

// atomicStore returns the store's guarded surface, or nil when it has none.
func (s *userAuthFactorService) atomicStore() factorSetStore {
	store, _ := s.kv.(factorSetStore)
	return store
}

// factorBatchStore is the multi-guarded batch a hard Delete commits through.
// kvstore.MultiGuardedBatchKVStore provides it.
type factorBatchStore interface {
	PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (applied bool, err error)
}

// batchStore returns the store's multi-guarded batch, or nil when it has none.
func (s *userAuthFactorService) batchStore() factorBatchStore {
	store, _ := s.kv.(factorBatchStore)
	return store
}

// FactorSetVersion returns the version of userID's factor set: 0 until the
// first membership change is committed, then moving on every Add, Remove and
// Rotate. It is the value a conditional add's ExpectedVersion is compared to.
// A store without a guarded write keeps no factor-set record and reports 0;
// a conditional add on it is refused with ErrUserAuthFactorAtomicStoreRequired.
func (s *userAuthFactorService) FactorSetVersion(ctx context.Context, userID uint64) (uint64, error) {
	if userID == 0 {
		return 0, &auth.InvalidFactorError{Reason: "userID must be non-zero"}
	}
	if s.atomicStore() == nil {
		return 0, nil
	}
	set, _, err := s.readFactorSet(ctx, userID)
	if err != nil {
		return 0, err
	}
	return set.Version, nil
}

// AddAuthFactor adds factor for userID through svc: with Add when cond is
// nil, with AddConditional otherwise. It returns the factor-set version a
// conditional add committed, zero for an unconditional one. Every transport's
// add handler calls it, so an absent condition and a present one reach the
// service the same way on each.
func AddAuthFactor(ctx context.Context, svc UserAuthFactorServicer, userID uint64, factor *auth.AuthFactor, cond *auth.FactorAddCondition) (uint64, error) {
	if cond == nil {
		return 0, svc.Add(ctx, userID, factor)
	}
	return svc.AddConditional(ctx, userID, factor, *cond)
}

// ListAuthFactors returns userID's factors and its factor-set version, read
// BEFORE the factors: a set that changes in between leaves the version
// behind the list, so an add guarded on it is refused rather than decided
// against a set the caller never saw.
func ListAuthFactors(ctx context.Context, svc UserAuthFactorServicer, userID uint64) ([]*auth.AuthFactor, uint64, error) {
	version, err := svc.FactorSetVersion(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	factors, err := svc.List(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	return factors, version, nil
}

// readFactorSet returns userID's stored factor-set record and the exact bytes
// it was decoded from, which a commit guards on. raw is nil when no record
// exists; the set is then version 0 with no committed ids.
func (s *userAuthFactorService) readFactorSet(ctx context.Context, userID uint64) (set *factorSet, raw []byte, err error) {
	key := factorSetKey(userID)
	raw, err = s.getRecord(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	set = &factorSet{}
	if len(raw) == 0 {
		return set, nil, nil
	}
	if err := json.Unmarshal(raw, set); err != nil {
		return nil, nil, &FactorSetDecodeError{Key: key, Err: err}
	}
	return set, raw, nil
}

// getRecord reads key for a factor-set decision. Unlike the display reads,
// which treat any error as absent, only absence is absence here: the
// KVServicer's *KeyNotFoundError and kvstore.KVStore's (nil, nil) are the two
// ways AuthFactorKV reports it, and every other failure fails the decision.
func (s *userAuthFactorService) getRecord(ctx context.Context, key string) ([]byte, error) {
	raw, err := s.kv.Get(ctx, key)
	if _, absent := errors.AsType[*KeyNotFoundError](err); absent {
		return nil, nil
	}
	return raw, err
}

// liveFactorSet narrows set.Active to the factors of userID active now: every
// committed id and every id the user index names, each kept only when its
// primary record exists, belongs to userID and is not revoked. A read failure
// fails the call; it never counts as "inactive".
func (s *userAuthFactorService) liveFactorSet(ctx context.Context, userID uint64, set *factorSet) error {
	prefix := userIndexPrefix(userID)
	keys, err := s.kv.List(ctx, prefix)
	if err != nil {
		return err
	}
	ids := make(map[string]struct{}, len(set.Active)+len(keys))
	for id := range set.Active {
		ids[id] = struct{}{}
	}
	for _, k := range keys {
		ids[strings.TrimPrefix(k, prefix)] = struct{}{}
	}
	live := make(map[string]auth.AuthFactorType, len(ids))
	for id := range ids {
		key := primaryKey(id)
		raw, err := s.getRecord(ctx, key)
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			continue
		}
		var f auth.AuthFactor
		if err := json.Unmarshal(raw, &f); err != nil {
			return &FactorSetDecodeError{Key: key, Err: err}
		}
		if f.UserID == userID && f.IsActive() {
			live[id] = f.Type
		}
	}
	set.Active = live
	return nil
}

// commitFactorSet applies change to userID's live factor set and commits the
// result, one version later, guarded on the exact record the change was
// decided against. change returning an error refuses the commit: nothing is
// written and the error is returned as is. A refused guard re-reads and
// decides again, so change must be a pure function of the set it is shown.
// It returns the committed version.
func (s *userAuthFactorService) commitFactorSet(ctx context.Context, store factorSetStore, userID uint64, change func(*factorSet) error) (uint64, error) {
	key := factorSetKey(userID)
	for range factorSetCommitAttempts {
		set, raw, err := s.readFactorSet(ctx, userID)
		if err != nil {
			return 0, err
		}
		if err := s.liveFactorSet(ctx, userID, set); err != nil {
			return 0, err
		}
		if err := change(set); err != nil {
			return 0, err
		}
		set.Version++
		next, err := json.Marshal(set)
		if err != nil {
			return 0, &auth.InvalidFactorError{Reason: "marshal factor set: " + err.Error()}
		}
		var committed bool
		if raw == nil {
			committed, err = store.PutIfAbsent(ctx, key, next)
		} else {
			committed, err = store.CompareAndSwap(ctx, key, raw, next)
		}
		if err != nil {
			return 0, err
		}
		if committed {
			return set.Version, nil
		}
	}
	return 0, &FactorSetContendedError{UserID: userID, Attempts: factorSetCommitAttempts}
}

// commitEnrollment adds factor, whose primary record add has written as data,
// to its user's factor set and publishes its index entries in ONE
// multi-guarded batch. The batch is guarded on the factor-set record the
// decision was made against, on the primary record still holding data, and on
// every index entry being absent. A hard Delete commits its own batch over the
// same primary and factor-set record, so the two serialise: a Delete that
// lands first leaves this enrollment refused with no index entry written, and
// one that lands after removes the entries this batch wrote. Writing the
// indexes after the commit instead would let them outlive a Delete that landed
// in between, naming a deleted factor and keeping its credential reserved.
//
// It returns the committed version, *auth.FactorNotFoundError when the primary
// record no longer holds data, and *auth.FactorAlreadyExistsError when an
// index entry has been claimed since add checked it.
func (s *userAuthFactorService) commitEnrollment(ctx context.Context, store factorBatchStore, factor *auth.AuthFactor, data []byte, cond *auth.FactorAddCondition) (uint64, error) {
	setKey := factorSetKey(factor.UserID)
	primary := primaryKey(factor.ID)
	indexKeys := factorIndexKeys(factor)
	for range factorSetCommitAttempts {
		raw, err := s.getRecord(ctx, primary)
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(raw, data) {
			return 0, &auth.FactorNotFoundError{FactorID: factor.ID}
		}
		for _, key := range indexKeys {
			owner, err := s.getRecord(ctx, key)
			if err != nil {
				return 0, err
			}
			if len(owner) > 0 {
				return 0, &auth.FactorAlreadyExistsError{Type: factor.Type, FactorID: string(owner)}
			}
		}
		set, setRaw, err := s.readFactorSet(ctx, factor.UserID)
		if err != nil {
			return 0, err
		}
		if err := s.liveFactorSet(ctx, factor.UserID, set); err != nil {
			return 0, err
		}
		if cond != nil {
			if err := checkFactorCondition(set, factor.UserID, factor.Type, *cond); err != nil {
				return 0, err
			}
		}
		set.Active[factor.ID] = factor.Type
		set.Version++
		next, err := json.Marshal(set)
		if err != nil {
			return 0, &auth.InvalidFactorError{Reason: "marshal factor set: " + err.Error()}
		}

		guards := []kvstore.Guard{{Key: setKey, Value: setRaw}, {Key: primary, Value: data}}
		writes := []kvstore.KV{{Key: setKey, Value: next}}
		for _, key := range indexKeys {
			guards = append(guards, kvstore.Guard{Key: key})
			writes = append(writes, kvstore.KV{Key: key, Value: []byte(factor.ID)})
		}
		applied, err := store.PutAllIfAllUnchanged(ctx, guards, writes)
		if err != nil {
			return 0, err
		}
		if applied {
			return set.Version, nil
		}
	}
	return 0, &FactorSetContendedError{UserID: factor.UserID, Attempts: factorSetCommitAttempts}
}

// commitMembership records a membership change already written to the
// factor records -- a revocation, or a factor already indexed -- as a new
// version of userID's factor set. A store without a guarded write keeps no
// factor-set record, and the change is complete without one.
func (s *userAuthFactorService) commitMembership(ctx context.Context, userID uint64) error {
	store := s.atomicStore()
	if store == nil {
		return nil
	}
	_, err := s.commitFactorSet(ctx, store, userID, func(*factorSet) error { return nil })
	return err
}

// checkFactorCondition evaluates cond against the live set an add of a
// factorType factor for userID is being decided on.
func checkFactorCondition(set *factorSet, userID uint64, factorType auth.AuthFactorType, cond auth.FactorAddCondition) error {
	if cond.ExpectedVersion != nil && *cond.ExpectedVersion != set.Version {
		return &auth.FactorConditionFailedError{
			UserID:          userID,
			Type:            factorType,
			Condition:       auth.FactorConditionExpectedVersion,
			ExpectedVersion: *cond.ExpectedVersion,
			CurrentVersion:  set.Version,
		}
	}
	if cond.NoActiveOfType {
		// Sorted so the factor a refusal names does not depend on map order.
		for _, id := range slices.Sorted(maps.Keys(set.Active)) {
			if set.Active[id] == factorType {
				return &auth.FactorConditionFailedError{
					UserID:           userID,
					Type:             factorType,
					Condition:        auth.FactorConditionNoActiveOfType,
					CurrentVersion:   set.Version,
					ExistingFactorID: id,
				}
			}
		}
	}
	return nil
}
