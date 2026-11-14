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

package firstboot

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// PendingAdminCertKey is the REPLICATED system store key under which the
// bootstrap admin's issued-or-adopted certificate (public material only,
// never the key) is durably recorded between ceremony initialize and first
// boot's binding (go-qrdb#548).
//
// It is a record in qrdb's own replicated store -- NOT the per-node
// seal/credential store, which cannot carry it (every cluster node holds its
// own system barrier and master key, never shared -- see
// docs/barrier-seal/architecture.md), and NOT a file in data_dir.
// Composers write it under the system tenant's identity namespace, the same
// store first boot's own role and user records live in.
const PendingAdminCertKey = "pending-admin-cert"

// pendingAdminCertConsumedMarker is the value ClearPendingAdminCert writes
// over the pending-admin-cert record once first boot has committed the
// binding it named, in place of the delete this used to be. It can never be
// mistaken for a real certPEM -- a PEM block always begins "-----BEGIN" --
// so AwaitPendingAdminCert (or any other reader) can tell "consumed" apart
// from "still pending" without a second field or record.
var pendingAdminCertConsumedMarker = []byte("consumed")

// pendingAdminCertSettleBudget bounds how long WriteIssuedAdminCertIfAny
// waits out the system shard electing a leader or moving it before the
// write's error stands. At the default tuning an election times out in 2 s;
// the budget covers a few rounds of it, a split vote and the new leader's
// ReadIndex warm-up. A variable so a test can shorten it.
var pendingAdminCertSettleBudget = 10 * time.Second

// pendingAdminCertSettleInterval is the pause between those attempts.
const pendingAdminCertSettleInterval = 100 * time.Millisecond

// PendingAdminCertStore is the minimal persistent KV surface
// WritePendingAdminCert / AwaitPendingAdminCert / ClearPendingAdminCert need.
// A composer's own guarded/namespaced KV store satisfies this directly: every
// production implementation (cmd/qrdbd's bootStore, sdk/go/node's
// systemPendingAdminCertStore) is backed by service.KVServicer /
// kvstore.AtomicKVStore, both of which already carry PutIfAbsent natively --
// Raft evaluates it inside the state machine, MVCC inside a transaction, so
// no new opcode is needed to make the write below a real create-if-absent
// (go-qrdb#548).
type PendingAdminCertStore interface {
	// PutIfAbsent atomically stores value under key only if key is absent,
	// reporting stored=true when this call created it and stored=false, with
	// a nil error, when a record was already there.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	Get(ctx context.Context, key string) ([]byte, error)
	// Put unconditionally overwrites key. ClearPendingAdminCert uses this,
	// never Delete, to transition the pending-admin-cert record to its
	// consumed state in place: the record is a one-way state machine
	// (pending, then consumed) and is never removed, so that
	// WritePendingAdminCert's PutIfAbsent can never recreate it once the
	// first binding has committed (go-qrdb#560 review item 8).
	Put(ctx context.Context, key string, value []byte) error
	// Delete is retained on the interface for implementors that need it
	// for other keys; ClearPendingAdminCert does not call it.
	Delete(ctx context.Context, key string) error
}

// WritePendingAdminCert durably records certPEM so AwaitPendingAdminCert can
// read it back even after a crash between the two calls. A nil store or an
// empty certPEM is a no-op: nothing was issued (or nothing the composer could
// have obtained yet) for this node's ceremony.
//
// The write is create-if-absent (go-qrdb#548): on a multi-node cluster, every
// node that ran the sealed-window ceremony calls this with ITS OWN issued
// credential, racing to record the one first boot will bind. A plain Put is
// last-writer-wins and can silently discard an earlier node's credential
// after that node's operator has already walked away with it believing it is
// the bound identity. PutIfAbsent makes exactly one of those writes win, and
// every later writer gets *AdminCredentialAlreadyPendingError instead of
// quietly overwriting the record -- nothing is issued to a credential that
// loses the race in the sense of being bound, but the credential itself was
// already minted by the CA before this call (ceremony.Initialize), so the
// caller is told plainly that ITS credential will not be the one bound and
// must not be distributed as if it will be.
func WritePendingAdminCert(ctx context.Context, store PendingAdminCertStore, certPEM []byte) error {
	if store == nil || len(certPEM) == 0 {
		return nil
	}
	stored, err := store.PutIfAbsent(ctx, PendingAdminCertKey, certPEM)
	if err != nil {
		return &PendingAdminCertStoreError{Op: "write", Cause: err}
	}
	if !stored {
		return &AdminCredentialAlreadyPendingError{}
	}
	return nil
}

// WriteIssuedAdminCertIfAny records core's last issued-or-adopted admin
// certificate into store as the pending-admin-cert hand-off, if core
// actually issued or adopted one on THIS node (go-qrdb#548 review item 6).
// A nil core, or one that has issued nothing, is a no-op.
//
// Shared by every composer that runs ceremony-initialize on this node and
// must hand its result to first boot across the sealed-window-to-full-boot
// transition -- cmd/qrdbd and sdk/go/node both call it at that seam, in
// place of each re-implementing the LastIssuedAdminCertPEM-then-write
// sequence by hand. Another node's write winning the create-if-absent race
// (*AdminCredentialAlreadyPendingError) is tolerated here, not returned:
// this node's credential simply is not the one first boot will bind, which
// is a normal outcome of the race WritePendingAdminCert documents, not a
// failure this call's caller needs to handle specially.
//
// It proposes nothing once the hand-off has happened. A composer's boot
// reruns this on every start, and an in-process recycle or unseal reuses the
// SAME core, which still holds the certificate it issued when the cluster was
// initialised (go-qrdb#662). The record only ever moves from absent to
// pending to consumed and is never removed, so a record this node's replica
// already holds -- whatever its value, however stale the read -- means
// PutIfAbsent can only lose. That case returns before any write, so a boot on
// an initialised cluster does not need a leader here at all.
//
// A write that must run waits out its shard electing a leader or moving it:
// an attempt whose error transient reports true is retried every
// pendingAdminCertSettleInterval until it lands, ctx ends, or
// pendingAdminCertSettleBudget passes; its error then stands. A nil transient
// retries nothing. Composers pass service.LeadershipTransient, which this
// package cannot import (pkg/service imports it). A timed-out attempt may
// still have committed; its retry then loses the create-if-absent race to
// itself, which is tolerated above, so the retry is idempotent.
func WriteIssuedAdminCertIfAny(ctx context.Context, core *ceremony.CeremonyService, store PendingAdminCertStore, transient func(error) bool) error {
	if core == nil || store == nil {
		return nil
	}
	certPEM, _ := core.LastIssuedAdminCertPEM()
	if len(certPEM) == 0 {
		return nil
	}
	if recorded, err := store.Get(ctx, PendingAdminCertKey); err == nil && len(recorded) > 0 {
		return nil
	}
	return writeSettled(ctx, transient, func() error {
		err := WritePendingAdminCert(ctx, store, certPEM)
		var alreadyPending *AdminCredentialAlreadyPendingError
		if errors.As(err, &alreadyPending) {
			return nil
		}
		return err
	})
}

// writeSettled runs write until it succeeds, its error is not transient,
// or pendingAdminCertSettleBudget runs out, waiting
// pendingAdminCertSettleInterval between attempts.
func writeSettled(ctx context.Context, transient func(error) bool, write func() error) error {
	deadline := time.Now().Add(pendingAdminCertSettleBudget)
	for {
		err := write()
		if err == nil {
			return nil
		}
		if transient == nil || !transient(err) || ctx.Err() != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(min(pendingAdminCertSettleInterval, remaining)):
		}
	}
}

// AwaitPendingAdminCert waits (bounded by timeout) for either the bootstrap
// admin to already hold a live certificate binding (nothing to do -- returns
// (nil, nil)) or the pending-admin-cert record to appear, and returns it.
//
// A composer calls this after ceremony initialize has run SOMEWHERE in the
// cluster -- possibly on a different node than the one running first boot --
// and before calling Initialize/BootstrapSystem, so AdminCertPEM is populated
// from the durable record rather than an in-process value that may not exist
// on this node.
//
// Returns *NoPendingAdminCredentialError on timeout, naming the retry
// command: the admin-credential step is retriable (re-run `qrdb init`) until
// the first binding commits (go-qrdb#548).
func AwaitPendingAdminCert(ctx context.Context, store PendingAdminCertStore, users AdminLookup, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	backoff := 50 * time.Millisecond
	for {
		if err := VerifyAdminCertBinding(dctx, users, time.Now().UTC()); err == nil {
			return nil, nil
		}
		if store != nil {
			if certPEM, err := store.Get(dctx, PendingAdminCertKey); err == nil && len(certPEM) > 0 &&
				!bytes.Equal(certPEM, pendingAdminCertConsumedMarker) {
				return certPEM, nil
			}
		}
		if !time.Now().Before(deadline) {
			return nil, &NoPendingAdminCredentialError{}
		}
		select {
		case <-dctx.Done():
			return nil, &NoPendingAdminCredentialError{}
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > time.Second {
			backoff = time.Second
		}
	}
}

// ClearPendingAdminCert marks the pending-admin-cert record consumed once
// first boot has committed the binding it named. It is a ONE-WAY state
// transition, pending -> consumed, implemented as an overwrite
// (pendingAdminCertConsumedMarker), never a delete: the record's key is
// NEVER removed once WritePendingAdminCert first creates it.
//
// This matters because WritePendingAdminCert's create-if-absent race
// (go-qrdb#548) is won exactly once, by whichever node's PutIfAbsent lands
// first -- but every node that ran the sealed-window ceremony holds its OWN
// CA-issued credential and may call WritePendingAdminCert again at any
// later time, including after this call has run for the winning node's
// record. A real delete left the key absent again, so a late-arriving
// node's PutIfAbsent would succeed a second time, creating a NEW pending
// record for a credential that will never be bound -- which some third
// node's AwaitPendingAdminCert could then read and hand to an operator as
// if it were authoritative (go-qrdb#560 review item 8). Overwriting instead
// of deleting keeps the key permanently present, so PutIfAbsent can never
// succeed again for it.
//
// Idempotent: consuming an already-consumed record, or calling with a nil
// store, is not an error.
//
// It writes the marker ONLY on a REAL pending -> consumed transition
// (go-qrdb#560 review B2): a prior version wrote it unconditionally, on
// every first-boot run, including the many nodes and many boots where
// nothing was ever pending at all (an auth-disabled node, for one -- it
// never runs a ceremony, so the key never exists -- but also, on ANY node,
// every boot after the first). That poisoned the key permanently: with the
// record present as "consumed" before a ceremony ever ran,
// WritePendingAdminCert's create-if-absent PutIfAbsent could never
// succeed again, so ceremony-initialize's admin-credential hand-off could
// never record a credential for first boot to bind -- the SAME failure
// bb65257df's repair fallthrough and a04e9d38d's auth-disabled carve-out
// were working around from the other side. Get's absence contract is not
// uniform across implementations (service.KVService's Get returns a
// *service.KeyNotFoundError-shaped error for a missing key;
// kvstore.MemoryKVStore's returns (nil, nil)); AwaitPendingAdminCert
// already treats EITHER shape as "nothing recorded yet", and this mirrors
// that same convention -- any error from Get, or an empty value, means
// there was nothing pending to consume, so nothing is written.
func ClearPendingAdminCert(ctx context.Context, store PendingAdminCertStore) error {
	if store == nil {
		return nil
	}
	certPEM, err := store.Get(ctx, PendingAdminCertKey)
	if err != nil || len(certPEM) == 0 || bytes.Equal(certPEM, pendingAdminCertConsumedMarker) {
		return nil
	}
	if err := store.Put(ctx, PendingAdminCertKey, pendingAdminCertConsumedMarker); err != nil {
		return &PendingAdminCertStoreError{Op: "clear", Cause: err}
	}
	return nil
}

// PendingAdminCertStoreError wraps a failure reading, writing or clearing the
// pending-admin-cert record in the replicated system store.
type PendingAdminCertStoreError struct {
	Op    string
	Cause error
}

// Error implements error.
func (e *PendingAdminCertStoreError) Error() string {
	return "firstboot: pending admin credential store " + e.Op + " failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *PendingAdminCertStoreError) Unwrap() error { return e.Cause }

// AdminCredentialAlreadyPendingError is returned by WritePendingAdminCert
// when another node (or an earlier call on this one) has already recorded a
// pending-admin-cert; see WritePendingAdminCert's create-if-absent race
// (go-qrdb#548). First boot binds only the record that won; the credential
// this call carried is not bound and must not be handed to an operator as
// if it is.
type AdminCredentialAlreadyPendingError struct{}

// Error implements error.
func (e *AdminCredentialAlreadyPendingError) Error() string {
	return "firstboot: another node already recorded a pending admin credential; " +
		"this one will not be bound -- discard it and use the credential the winning node reports"
}

// NoPendingAdminCredentialError is returned by AwaitPendingAdminCert when
// neither an admin binding nor a pending certificate record appeared within
// the bound. The admin-credential step is retriable (go-qrdb#548): re-running
// `qrdb init` -- with exactly one of --admin-csr, --admin-cert or
// --admin-generate; there is no credential-less default -- issues a fresh
// credential and writes a new pending record, as long as no binding has
// committed yet.
type NoPendingAdminCredentialError struct{}

// Error implements error.
func (e *NoPendingAdminCredentialError) Error() string {
	return "firstboot: no admin certificate binding and no pending admin credential record; " +
		`re-run "qrdb init" (with exactly one of --admin-csr, --admin-cert or --admin-generate) to retry the admin-credential step`
}
