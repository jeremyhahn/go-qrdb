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
	"bytes"
	"context"
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// leaseOwnerKeyPrefix roots the ownership records of namespaced leases. It is
// in the internal key namespace and outside every (tenant, database)
// namespace, so no caller can read, write or forge one through a namespaced
// view.
const leaseOwnerKeyPrefix = service.InternalKeyPrefixSystem + "lease-owner/"

// namespacedLeaseService confines leases to the composite (tenant, db)
// namespace that granted them.
//
// Lease IDs are globally unique integers allocated by the shared Lessor inside
// the state machine, so the ID alone says nothing about who owns a lease. At
// Grant the view therefore attaches an ownership record to the new lease: the
// key leaseOwnerKeyPrefix + namespace + ID, written with the lease through the
// MVCC engine. Because the record is attached, the lessor deletes it when the
// lease is revoked or expires, and it is replicated with the lease -- every
// node agrees on the owner.
//
// A lease is this namespace's only when its attached keys include this
// namespace's record for its ID. Leases lists only those; KeepAlive and
// TimeToLive answer any other ID -- another tenant's, another database's, or
// one granted outside a namespaced view -- with the same
// service.LeaseNotFoundError a missing lease gets, and Revoke with the same
// idempotent success. Attached keys are presented with the namespace prefix
// stripped, and keys outside the namespace, the ownership record among them,
// are never shown.
type namespacedLeaseService struct {
	inner  service.LeaseServicer
	mvcc   service.MVCCKVServicer
	prefix string
}

// Compile-time interface checks.
var (
	_ service.LeaseServicer = (*namespacedLeaseService)(nil)
	_ leaseOwnership        = (*namespacedLeaseService)(nil)
)

// newNamespacedLeaseService creates a LeaseServicer scoped to the given
// composite namespace prefix. mvcc is the engine the ownership record is
// written through; without one the view refuses to grant.
func newNamespacedLeaseService(inner service.LeaseServicer, mvcc service.MVCCKVServicer, prefix string) *namespacedLeaseService {
	return &namespacedLeaseService{inner: inner, mvcc: mvcc, prefix: prefix}
}

// ownerKey returns this namespace's ownership record for lease id.
func (s *namespacedLeaseService) ownerKey(id int64) []byte {
	return []byte(leaseOwnerKeyPrefix + s.prefix + strconv.FormatInt(id, 10))
}

// owns reports whether l carries this namespace's ownership record.
func (s *namespacedLeaseService) owns(l *lease.Lease) bool {
	if l == nil {
		return false
	}
	record := s.ownerKey(l.ID)
	for _, k := range l.Keys {
		if bytes.Equal(k, record) {
			return true
		}
	}
	return false
}

// stripLease returns a shallow copy of the lease with attached keys that fall
// under the namespace stripped of their prefix. Keys outside the namespace are
// omitted.
func (s *namespacedLeaseService) stripLease(l *lease.Lease) *lease.Lease {
	if l == nil {
		return nil
	}
	out := *l
	p := []byte(s.prefix)
	keys := make([][]byte, 0, len(l.Keys))
	for _, k := range l.Keys {
		if bytes.HasPrefix(k, p) {
			keys = append(keys, bytes.TrimPrefix(k, p))
		}
	}
	out.Keys = keys
	return &out
}

// owned returns lease id when this namespace owns it, and
// service.LeaseNotFoundError otherwise.
func (s *namespacedLeaseService) owned(ctx context.Context, id int64) (*lease.Lease, error) {
	l, err := s.inner.TimeToLive(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.owns(l) {
		return nil, &service.LeaseNotFoundError{LeaseID: id}
	}
	return l, nil
}

// requireOwned implements leaseOwnership.
func (s *namespacedLeaseService) requireOwned(ctx context.Context, leaseID int64) error {
	_, err := s.owned(ctx, leaseID)
	return err
}

// Grant creates a new lease with the specified TTL and records this namespace
// as its owner. A lease whose ownership cannot be recorded is revoked and the
// failure returned, so no lease exists that no namespace owns.
func (s *namespacedLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	if s.mvcc == nil {
		return nil, &service.CapabilityUnsupportedError{Message: "lease: this database has no MVCC engine to record lease ownership"}
	}
	l, err := s.inner.Grant(ctx, ttl)
	if err != nil {
		return nil, err
	}
	if _, err := s.mvcc.PutWithLease(ctx, string(s.ownerKey(l.ID)), nil, l.ID); err != nil {
		_ = s.inner.Revoke(ctx, l.ID)
		return nil, err
	}
	return s.stripLease(l), nil
}

// Revoke immediately expires a lease this namespace owns and deletes all
// attached keys.
//
// Revoking a lease that does not exist is an idempotent success, so revoking
// a lease this namespace does not own is the same no-op: answering it
// differently would tell the caller the ID is live elsewhere. The no-op is
// answered here rather than by the inner service, because lease IDs are
// allocated in sequence and a not-yet-granted ID passed through could revoke
// another namespace's lease granted in between.
func (s *namespacedLeaseService) Revoke(ctx context.Context, leaseID int64) error {
	if _, err := s.owned(ctx, leaseID); err != nil {
		var notFound *service.LeaseNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return err
	}
	return s.inner.Revoke(ctx, leaseID)
}

// KeepAlive resets the TTL on a lease this namespace owns.
func (s *namespacedLeaseService) KeepAlive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	if _, err := s.owned(ctx, leaseID); err != nil {
		return nil, err
	}
	l, err := s.inner.KeepAlive(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	return s.stripLease(l), nil
}

// TimeToLive returns the current state of a lease this namespace owns, with
// attached keys presented within the namespace.
func (s *namespacedLeaseService) TimeToLive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	l, err := s.owned(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	return s.stripLease(l), nil
}

// Leases returns the active leases this namespace owns, each with its
// attached keys presented within the namespace.
func (s *namespacedLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	leases, err := s.inner.Leases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*lease.Lease, 0, len(leases))
	for _, l := range leases {
		if s.owns(l) {
			out = append(out, s.stripLease(l))
		}
	}
	return out, nil
}
