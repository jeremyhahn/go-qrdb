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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// quotaKVService wraps a KVServicer to enforce and account a tenant's
// storage-byte quota. A write is admitted through the Accountant first: when it
// would push the tenant over its MaxStorageBytes the call is rejected with
// *quota.ExceededError (mapped to HTTP 429) before it ever reaches consensus.
// Admitted writes add their value length to the in-memory accumulator; deletes
// subtract the freed bytes. The accumulator is flushed to the persisted
// CurrentStorageBytes counter by the background reconciler (bounded staleness by
// design — the reconciler corrects drift for volume tenants).
//
// It is installed only for tenants with a positive storage limit, so the
// unlimited and system-tenant paths remain plain passthroughs with no quota
// overhead on the hot path.
//
// Overwrites are counted by value length rather than the (key-dependent) delta
// against any prior value: computing the delta would require an extra read on
// the Put hot path. This over-counts overwrites; the reconciler corrects it for
// volume tenants and it is the documented approximation for shared tenants.
type quotaKVService struct {
	inner    service.KVServicer
	acct     *quota.Accountant
	tenantID string
	limit    int64
}

// Compile-time interface check.
var _ service.KVServicer = (*quotaKVService)(nil)

// newQuotaKVService wraps inner with storage-byte quota enforcement for the
// given tenant and limit (MaxStorageBytes).
func newQuotaKVService(inner service.KVServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaKVService {
	return &quotaKVService{inner: inner, acct: acct, tenantID: tenantID, limit: limit}
}

// reserve admits n bytes against the tenant's quota, returning *quota.ExceededError
// when the write would exceed the limit.
func (s *quotaKVService) reserve(ctx context.Context, n int) error {
	return s.acct.Reserve(ctx, s.tenantID, s.limit, int64(n))
}

// priorSize returns the byte length of the value currently stored under key, or
// zero when the key is absent or unreadable. It uses a linearizable read (Get,
// not GetStale): the delete delta must reflect the value as committed, and a
// stale read can miss a just-written-but-not-yet-locally-visible value — which
// would free zero bytes and leave the tenant's usage permanently inflated. The
// accountant floors effective usage at zero and the reconciler corrects any
// residual drift for volume tenants, but the linearizable read keeps the freed
// amount accurate at the source on the delete path.
func (s *quotaKVService) priorSize(ctx context.Context, key string) int64 {
	v, err := s.inner.Get(ctx, key)
	if err != nil {
		return 0
	}
	return int64(len(v))
}

// A failed write keeps its reservation only when its outcome is UNKNOWN.
//
// The reasoning is identical for each and was got wrong on each of them
// independently, so it is written once here and referenced below.
//
// A non-nil error from the inner store does not mean the write did not happen.
// For a Raft-backed service it frequently means the opposite: SyncPropose can
// commit the entry and then fail to deliver its response, so the value is
// durable and the caller was told nothing. Releasing the reservation there
// drops the accounting for a value that IS stored, and the tenant's usage sits
// permanently below what they hold — every later write measured against a
// limit that no longer bounds anything. That is quota failing OPEN.
//
// Retaining errs the other way: a write that genuinely did not land leaves its
// bytes reserved until the reconciler corrects it. Over-counting costs a tenant
// headroom they should have had; under-counting costs the limit its meaning,
// and only one of those is a quota failure.
//
// A DEFINITIVE refusal is different and still releases. PutIfAbsent returning
// (false, nil) means the guard was evaluated and declined, so nothing was
// written and nothing is owed. The distinction is between "no" and "unknown",
// which is exactly what the old `err != nil || !stored` collapsed
// (go-qrdb#90; the same defect was fixed for CompareAndSwap in #56).
//
// NOT every error is unknown, and treating them alike is its own defect in the
// opposite direction. An error raised BEFORE the write is proposed — a
// validation failure, an encoding failure, a refusal by a state machine that
// does not offer the operation — proves no value was stored. Retaining those
// bytes leaks quota on every rejected request, and for a PREFIX tenant nothing
// ever gives them back: the reconciler corrects volume tenants from real
// storage, so a prefix tenant's usage only ever climbs. A caller sending
// invalid requests in a loop could exhaust a tenant's quota without ever
// writing a byte, which is a denial of service built out of a safety measure.
//
// So the rule is three-way, not two:
//
//   - definitive REFUSAL (a guard evaluated and declined)  -> release
//   - definitive FAILURE (proven not written)              -> release
//   - anything else                                        -> retain
//
// writeProvenNotStored draws the second line, and it defaults to RETAIN for
// errors it does not recognise. That asymmetry is deliberate: an unrecognised
// error might have committed, and mistaking "unknown" for "failed" is the
// fail-open direction.
//
// The verbs that RESERVE are Put, PutIfAbsent, PutWithMode and PutEntity. The
// rest never hold a reservation across the inner call: CompareAndDelete and
// DeleteEntity release only on a definitive success, and CompareAndSwap only
// on a definitive refusal or success. That enumeration is written out because
// the first fix of this defect covered three of the four and left PutEntity
// failing open — a wrapper is only as fail-closed as its least-examined
// method.

// Put accounts len(value) bytes after admitting the write through the quota.
func (s *quotaKVService) Put(ctx context.Context, key string, value []byte) error {
	n := len(value)
	if err := s.reserve(ctx, n); err != nil {
		return err
	}
	if err := s.inner.Put(ctx, key, value); err != nil {
		s.releaseIfProvenNotStored(err, n)
		return err
	}
	return nil
}

// PutIfAbsent reserves quota, attempts the conditional write, and releases the
// reservation when the write did not take effect.
//
// The release on stored=false is the part that matters. A declined conditional
// write stores nothing, so charging the tenant for it would let a caller
// exhaust another tenant's — or their own — quota by racing for a key that
// already exists, without ever writing a byte. Reserving first is still
// required: admitting the write only after it has landed would let a tenant
// exceed their limit whenever two writes are in flight.
func (s *quotaKVService) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	n := len(value)
	if err := s.reserve(ctx, n); err != nil {
		return false, err
	}
	stored, err := s.inner.PutIfAbsent(ctx, key, value)
	if err != nil {
		s.releaseIfProvenNotStored(err, n)
		return stored, err
	}
	if !stored {
		// Definitively declined: the key was already there and nothing was
		// written, so nothing is owed.
		s.acct.Release(s.tenantID, int64(n))
		return false, nil
	}
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, releasing the deleted value's quota when it does. A refused
// delete changes nothing, so it releases nothing.
func (s *quotaKVService) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	deleted, err := s.inner.CompareAndDelete(ctx, key, expected)
	if err != nil || !deleted {
		return deleted, err
	}
	s.acct.Release(s.tenantID, int64(len(expected)))
	return true, nil
}

// CompareAndSwap replaces key's value only when it still equals expected,
// accounting the DELTA between the two.
//
// No priorSize read is needed here, unlike Delete: the guard is what makes the
// delta exact. The swap lands only if the stored value equals expected, so
// len(expected) IS the currently accounted size, and len(next)-len(expected)
// is the change — read from the arguments rather than from a value that could
// move between the read and the write.
//
// Growth is reserved BEFORE the write and released if the swap does not
// happen, for the same reason PutIfAbsent reserves first: admitting the write
// only after it lands would let a tenant exceed their limit whenever two swaps
// are in flight. A shrink is released only after the swap succeeds, because a
// refused swap frees nothing.
//
// An ERROR is not a refusal. AtomicKVStore says a non-nil error leaves the
// outcome unknown, and for a Raft-backed store that is a real state, not a
// formality: SyncPropose can commit the entry and then fail to deliver its
// response. Releasing the reservation there would drop the accounting for a
// value that IS stored, and the tenant's usage would sit permanently below
// what they hold — every later write measured against a limit that no longer
// bounds anything.
//
// So the reservation is retained on error. That errs toward over-counting: a
// swap that genuinely did not land leaves the growth reserved until the
// reconciler corrects it. Over-counting costs a tenant headroom they should
// have had; under-counting costs the limit its meaning, and only one of those
// is a quota failure (go-qrdb#56).
func (s *quotaKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	growth := len(next) - len(expected)
	if growth > 0 {
		if err := s.reserve(ctx, growth); err != nil {
			return false, err
		}
	}
	swapped, err := s.inner.CompareAndSwap(ctx, key, expected, next)
	if err != nil {
		return swapped, err
	}
	if !swapped {
		// Definitively refused: nothing was written, so nothing is owed.
		if growth > 0 {
			s.acct.Release(s.tenantID, int64(growth))
		}
		return false, nil
	}
	if growth < 0 {
		s.acct.Release(s.tenantID, int64(-growth))
	}
	return true, nil
}

// PutWithMode accounts len(value) bytes after admitting the write.
func (s *quotaKVService) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	n := len(value)
	if err := s.reserve(ctx, n); err != nil {
		return err
	}
	if err := s.inner.PutWithMode(ctx, key, value, mode); err != nil {
		s.releaseIfProvenNotStored(err, n)
		return err
	}
	return nil
}

// Delete subtracts the freed bytes from the accumulator on success.
func (s *quotaKVService) Delete(ctx context.Context, key string) error {
	n := s.priorSize(ctx, key)
	if err := s.inner.Delete(ctx, key); err != nil {
		return err
	}
	s.acct.Sub(s.tenantID, n)
	return nil
}

// DeleteWithMode subtracts the freed bytes from the accumulator on success.
func (s *quotaKVService) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	n := s.priorSize(ctx, key)
	if err := s.inner.DeleteWithMode(ctx, key, mode); err != nil {
		return err
	}
	s.acct.Sub(s.tenantID, n)
	return nil
}

// PutEntity accounts len(req.Data) bytes after admitting the write.
func (s *quotaKVService) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	n := len(req.Data)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.PutEntity(ctx, req)
	if err != nil {
		// The entity path both VALIDATES before proposing and, once past that,
		// delegates to the same Raft-backed Put — so it produces errors of
		// both kinds and needs the same classification as the plain verbs.
		s.releaseIfProvenNotStored(err, n)
		return nil, err
	}
	return resp, nil
}

// DeleteEntity subtracts the freed bytes from the accumulator on success.
func (s *quotaKVService) DeleteEntity(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	n := s.priorSize(ctx, req.ID)
	resp, err := s.inner.DeleteEntity(ctx, req)
	if err != nil {
		return nil, err
	}
	s.acct.Sub(s.tenantID, n)
	return resp, nil
}

// ---------------------------------------------------------------------------
// Read-path passthroughs (no accounting).
// ---------------------------------------------------------------------------

func (s *quotaKVService) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *quotaKVService) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *quotaKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.inner.Scan(ctx, prefix)
}

func (s *quotaKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return s.inner.GetStale(ctx, key)
}

func (s *quotaKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.ListStale(ctx, prefix)
}

func (s *quotaKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.inner.ScanStale(ctx, prefix)
}

func (s *quotaKVService) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	return s.inner.GetWithConsistency(ctx, key, opts)
}

func (s *quotaKVService) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	return s.inner.ListWithConsistency(ctx, prefix, opts)
}

func (s *quotaKVService) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	return s.inner.ScanWithConsistency(ctx, prefix, opts)
}

func (s *quotaKVService) ExistsWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (bool, error) {
	return s.inner.ExistsWithConsistency(ctx, key, opts)
}

func (s *quotaKVService) GetEntity(ctx context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return s.inner.GetEntity(ctx, req)
}

func (s *quotaKVService) ListEntities(ctx context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return s.inner.ListEntities(ctx, req)
}

// releaseIfProvenNotStored gives back a reservation only when err proves the
// write did not happen. See the note above Put for why the default is to keep
// it.
func (s *quotaKVService) releaseIfProvenNotStored(err error, n int) {
	if writeProvenNotStored(err) {
		s.acct.Release(s.tenantID, int64(n))
	}
}

// writeProvenNotStored reports whether err was raised BEFORE anything could be
// written, so the reservation is certainly owed back.
//
// Every case here is an error the service produces without reaching consensus,
// or one a state machine returns having applied nothing:
//
//   - EncodeRequestError: the command could not be built, so it was never
//     proposed.
//   - the entity validation family: rejected on the request, before any
//     proposal.
//   - NotLeaderError: the proposal was refused rather than accepted. A leader
//     elsewhere may later write something, but not THIS call's bytes.
//   - SMNotConfiguredError: the entry applied, and the subsystem it needed was
//     absent, so it stored nothing.
//   - ErrAtomicWriteUnsupported: the capability was refused up front.
//
// Anything else — a ProposeError wrapping a transport failure, a deadline, a
// cancelled context — may have committed, and is treated as unknown.
func writeProvenNotStored(err error) bool {
	var (
		encodeErr     *service.EncodeRequestError
		emptyType     *service.EntityEmptyTypeError
		emptyID       *service.EntityEmptyIDError
		emptyData     *service.EntityEmptyDataError
		notLeader     *service.NotLeaderError
		notConfigured *service.SMNotConfiguredError
	)
	switch {
	case errors.As(err, &encodeErr),
		errors.As(err, &emptyType),
		errors.As(err, &emptyID),
		errors.As(err, &emptyData),
		errors.As(err, &notLeader),
		errors.As(err, &notConfigured),
		errors.Is(err, kvstore.ErrAtomicWriteUnsupported):
		return true
	default:
		return false
	}
}
