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

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// namespacedLeaseService wraps a LeaseServicer so that the keys attached to a
// lease are presented within the composite (tenant, db) namespace: returned
// leases have the namespace prefix stripped from their attached keys.
//
// Lease IDs are globally unique integers allocated by the shared Lessor inside
// the state machine; they are NOT part of the key namespace. Grant, Revoke,
// KeepAlive, TimeToLive and Leases() therefore operate on the global lease set
// (a lease is a cluster-wide TTL handle, not a per-database object) — only the
// VISIBILITY of each lease's attached keys is scoped to the namespace. A
// freshly granted lease has no attached keys yet, so the list must not filter
// leases by namespace membership (doing so hid keyless leases and was
// inconsistent with the global Grant/Revoke/KeepAlive ID semantics).
type namespacedLeaseService struct {
	inner  service.LeaseServicer
	prefix string
}

// Compile-time interface check.
var _ service.LeaseServicer = (*namespacedLeaseService)(nil)

// newNamespacedLeaseService creates a LeaseServicer scoped to the given
// composite namespace prefix.
func newNamespacedLeaseService(inner service.LeaseServicer, prefix string) *namespacedLeaseService {
	return &namespacedLeaseService{inner: inner, prefix: prefix}
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

// Grant creates a new lease with the specified TTL.
func (s *namespacedLeaseService) Grant(ctx context.Context, ttl int64) (*lease.Lease, error) {
	return s.inner.Grant(ctx, ttl)
}

// Revoke immediately expires a lease and deletes all attached keys.
func (s *namespacedLeaseService) Revoke(ctx context.Context, leaseID int64) error {
	return s.inner.Revoke(ctx, leaseID)
}

// KeepAlive resets the TTL on an existing lease.
func (s *namespacedLeaseService) KeepAlive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	l, err := s.inner.KeepAlive(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	return s.stripLease(l), nil
}

// TimeToLive returns the current state of a lease with attached keys presented
// within the namespace.
func (s *namespacedLeaseService) TimeToLive(ctx context.Context, leaseID int64) (*lease.Lease, error) {
	l, err := s.inner.TimeToLive(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	return s.stripLease(l), nil
}

// Leases returns the active leases (the global lease set), with each lease's
// attached keys stripped to those under this namespace. Leases are global TTL
// handles, so the set is not filtered by namespace membership — a keyless,
// freshly granted lease is still reported.
func (s *namespacedLeaseService) Leases(ctx context.Context) ([]*lease.Lease, error) {
	leases, err := s.inner.Leases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*lease.Lease, 0, len(leases))
	for _, l := range leases {
		out = append(out, s.stripLease(l))
	}
	return out, nil
}
