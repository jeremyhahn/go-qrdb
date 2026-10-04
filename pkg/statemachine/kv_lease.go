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

package statemachine

import (
	"errors"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// Durable leases for every KVStateMachine.
//
// A KVStateMachine keeps its leases in a lease.Lessor whose durable half -- the
// lease table under lease.TablePrefix -- lives in the state machine's own
// keyspace, exactly as it does for the pebble state machine and the MVCC
// engine. Every lease mutation made during apply (a grant, a revoke, a key
// bound to a lease or released from one) is staged into the SAME
// Backend.ApplyAtomic set as the data write that caused it, so the two commit
// together or not at all, on every backend: the in-memory ones, the sharded
// ones, the fast cache and the disk state machine.
//
// Because the table is ordinary keyspace content, a snapshot carries it and a
// replica hash covers it. The Lessor's in-memory state is rebuilt from it at
// the three moments the keyspace changes without passing through apply: when
// an in-memory state machine is created (resetLessor, over its empty
// keyspace), when the disk state machine opens its storage (DiskAdapter.Open),
// and after any state machine recovers from a snapshot (RecoverSnapshot).
//
// The keys are the lease table's own: clients may not write them
// (validateClientKey) and enumeration does not return them (isStateMachineKey).

// mutationWriter is a lease.Writer that stages the lease table's records as
// backend mutations, so they join the apply set of the write that caused them.
type mutationWriter struct {
	muts *[]Mutation
}

// Set stages key = value.
func (w mutationWriter) Set(key, value []byte) error {
	*w.muts = append(*w.muts, Mutation{Key: key, Value: value})
	return nil
}

// Delete stages the removal of key.
func (w mutationWriter) Delete(key []byte) error {
	*w.muts = append(*w.muts, Mutation{Key: key, Delete: true})
	return nil
}

// Compile-time interface check.
var _ lease.Writer = mutationWriter{}

// commitLeased applies muts all or nothing together with the lease binding of
// every key they write: a put binds its key to leases[i] (lease.NoLease
// releases it), and a delete releases its key. leases may be nil, meaning no
// put names a lease. Every lease the set names must already have been checked
// to exist (leaseRefusal), so Bind's not-found is a backstop, not the refusal.
//
// Bind updates the Lessor's in-memory state as it stages; a failed commit is a
// storage fault that halts apply on this replica, as it does for pebble.
func (k *KVStateMachine) commitLeased(muts []Mutation, leases []int64) error {
	all := make([]Mutation, 0, 2*len(muts))
	all = append(all, muts...)
	w := mutationWriter{muts: &all}
	for i, m := range muts {
		id := lease.NoLease
		if !m.Delete && leases != nil {
			id = leases[i]
		}
		if err := k.lessor.Bind(w, m.Key, id); err != nil {
			return err
		}
	}
	return k.backend.ApplyAtomic(all)
}

// bound reports whether key is currently bound to a lease. A write of a bound
// key must go through commitLeased so its release from that lease commits
// with it; a write of an unbound key touches no lease record.
func (k *KVStateMachine) bound(key []byte) bool {
	return k.lessor.LeaseOf(key) != lease.NoLease
}

// leaseRefusal returns the result that refuses a write naming a lease this
// state machine does not hold, as an application error every replica returns
// alike. lease.NoLease is always accepted.
func (k *KVStateMachine) leaseRefusal(leaseIDs ...int64) (sm.Result, bool) {
	for _, id := range leaseIDs {
		if id != lease.NoLease && !k.lessor.Has(id) {
			return sm.Result{Data: protocol.EncodeLeaseNotFoundError(id)}, true
		}
	}
	return sm.Result{}, false
}

// restoreLessor rebuilds the Lessor's in-memory state from the lease table in
// the backend. A table that does not read or validate is returned and leaves
// the Lessor as it was: the table is the only record of which keys a lease
// deletes, so guessing past a damaged one is never safe.
func (k *KVStateMachine) restoreLessor() error {
	table, err := k.backend.Scan([]byte(lease.TablePrefix), 0)
	if err != nil {
		return &StorageError{Op: "lease_table_read", Inner: err}
	}
	view := memstore.NewStorage()
	for key, value := range table {
		if err := view.Set([]byte(key), value); err != nil {
			return &StorageError{Op: "lease_table_read", Inner: err}
		}
	}
	return k.lessor.Restore(view)
}

// resetLessor is restoreLessor for a state machine created over an empty
// keyspace, where there is no table to fail on. It is called from the
// create functions, which cannot return an error; a failure there means the
// backend cannot be read at all, and a replica that cannot read its own
// keyspace cannot start.
func (k *KVStateMachine) resetLessor() {
	if err := k.restoreLessor(); err != nil {
		panic("statemachine: failed to reset the lessor over a new keyspace: " + err.Error())
	}
}

// handleLeaseGrant grants a lease and records it, with the advanced lease-id
// sequence, in the lease table.
func (k *KVStateMachine) handleLeaseGrant(payload []byte, _ uint64) (sm.Result, error) {
	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease grant: " + err.Error()}
	}
	var muts []Mutation
	info, grantErr := k.lessor.Grant(mutationWriter{muts: &muts}, ttl)
	if grantErr != nil {
		// A table write fault would be local to this replica and must halt
		// apply; an invalid TTL is refused alike on every replica.
		var twe *lease.TableWriteError
		if errors.As(grantErr, &twe) {
			return sm.Result{}, grantErr
		}
		return RefusalResult(grantErr), nil
	}
	if err := k.backend.ApplyAtomic(muts); err != nil {
		return sm.Result{}, &StorageError{Op: "lease_grant", Inner: err}
	}
	return sm.Result{Value: uint64(info.ID)}, nil
}

// handleLeaseRevoke revokes a lease and deletes every key still bound to it,
// in one atomic set with the lease-table removals. A key re-put under another
// lease, or with none, since it was bound here has already left the lease and
// is untouched. Revoking a lease that no longer exists succeeds and changes
// nothing, so a replayed or duplicate revoke -- the expiry sweeper's, or a
// client's racing it -- never stalls apply.
func (k *KVStateMachine) handleLeaseRevoke(payload []byte, revision uint64) (sm.Result, error) {
	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease revoke: " + err.Error()}
	}
	var muts []Mutation
	keys, revokeErr := k.lessor.Revoke(mutationWriter{muts: &muts}, leaseID)
	if revokeErr != nil {
		var nfe *lease.NotFoundError
		if errors.As(revokeErr, &nfe) {
			return sm.Result{Value: revision}, nil
		}
		return sm.Result{}, revokeErr
	}
	deleted := make([]Mutation, len(keys))
	for i, key := range keys {
		deleted[i] = Mutation{Key: key, Delete: true}
	}
	muts = append(muts, deleted...)
	if err := k.backend.ApplyAtomic(muts); err != nil {
		return sm.Result{}, &StorageError{Op: "lease_revoke", Inner: err}
	}
	notifyMutations(k.watchManager, deleted, revision)
	return sm.Result{Value: revision}, nil
}

// handleLeaseKeepAlive resets a lease's expiry. Expiry is wall-clock state
// local to each replica, so nothing is written. A lease that no longer exists
// is an idempotent no-op, for the same reason as handleLeaseRevoke.
func (k *KVStateMachine) handleLeaseKeepAlive(payload []byte, revision uint64) (sm.Result, error) {
	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease keep-alive: " + err.Error()}
	}
	if _, kaErr := k.lessor.KeepAlive(leaseID); kaErr != nil {
		var nfe *lease.NotFoundError
		if errors.As(kaErr, &nfe) {
			return sm.Result{Value: revision}, nil
		}
		return sm.Result{}, kaErr
	}
	return sm.Result{Value: revision}, nil
}

// handleNativeMVCCPut serves OpMVCCPut on a state machine with no MVCCStore:
// a put that binds its key to the lease it names (lease.NoLease releases the
// key from any lease). It is the lease-bearing write the service layer's
// PutWithLease proposes, and what the pebble state machine serves the same
// opcode as -- a put with no revision history.
func (k *KVStateMachine) handleNativeMVCCPut(payload []byte, revision uint64) (sm.Result, error) {
	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc put: " + err.Error()}
	}
	if kerr := validateClientKey("put", key); kerr != nil {
		return sm.Result{}, kerr
	}
	if res, refused := k.leaseRefusal(leaseID); refused {
		return res, nil
	}
	muts := []Mutation{{Key: key, Value: value}}
	if err := k.commitLeased(muts, []int64{leaseID}); err != nil {
		return sm.Result{}, &StorageError{Op: "put", Inner: err}
	}
	notifyMutations(k.watchManager, muts, revision)
	return sm.Result{Value: revision}, nil
}

// handleNativeMVCCDelete serves OpMVCCDelete on a state machine with no
// MVCCStore: a delete that releases its key from any lease.
func (k *KVStateMachine) handleNativeMVCCDelete(payload []byte, revision uint64) (sm.Result, error) {
	key, err := protocol.DecodeMVCCDelete(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc delete: " + err.Error()}
	}
	if kerr := validateClientKey("delete", key); kerr != nil {
		return sm.Result{}, kerr
	}
	muts := []Mutation{{Key: key, Delete: true}}
	if err := k.commitLeased(muts, nil); err != nil {
		return sm.Result{}, &StorageError{Op: "delete", Inner: err}
	}
	notifyMutations(k.watchManager, muts, revision)
	return sm.Result{Value: revision}, nil
}

// lookupLease answers the lease read queries from the Lessor.
func (k *KVStateMachine) lookupLease(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case *lease.LeaseTimeToLiveQuery:
		l, err := k.lessor.LookupOrNotFound(q.ID)
		return l, true, err
	case lease.LeaseTimeToLiveQuery:
		l, err := k.lessor.LookupOrNotFound(q.ID)
		return l, true, err
	case *lease.LeasesQuery, lease.LeasesQuery:
		return k.lessor.Leases(), true, nil
	default:
		return nil, false, nil
	}
}
