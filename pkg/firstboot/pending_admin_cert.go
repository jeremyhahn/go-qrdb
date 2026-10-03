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
	"context"
	"time"
)

// PendingAdminCertKey is the REPLICATED system store key under which the
// bootstrap admin's issued-or-adopted certificate (public material only,
// never the key) is durably recorded between ceremony initialize and first
// boot's binding (go-qrdb#548).
//
// It is a record in qrdb's own replicated store -- NOT the per-node
// seal/credential store, which cannot carry it (every cluster node holds its
// own system barrier and master key, never shared -- see
// docs/seal/per-node-barrier-architecture.md), and NOT a file in data_dir.
// Composers write it under the system tenant's identity namespace, the same
// store first boot's own role and user records live in.
const PendingAdminCertKey = "pending-admin-cert"

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
			if certPEM, err := store.Get(dctx, PendingAdminCertKey); err == nil && len(certPEM) > 0 {
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

// ClearPendingAdminCert deletes the pending-admin-cert record once first
// boot has committed the binding. Idempotent: deleting an already-absent
// record, or calling with a nil store, is not an error.
func ClearPendingAdminCert(ctx context.Context, store PendingAdminCertStore) error {
	if store == nil {
		return nil
	}
	if err := store.Delete(ctx, PendingAdminCertKey); err != nil {
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
// `qrdb init` -- with --admin-csr, --admin-cert, or no flags to generate --
// issues a fresh credential and writes a new pending record, as long as no
// binding has committed yet.
type NoPendingAdminCredentialError struct{}

// Error implements error.
func (e *NoPendingAdminCredentialError) Error() string {
	return "firstboot: no admin certificate binding and no pending admin credential record; " +
		`re-run "qrdb init" (with --admin-csr, --admin-cert, or no flags to generate) to retry the admin-credential step`
}
