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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sharedLessor is one lessor shared by every tenant and database, as the
// system shard's is: lease IDs are global, a key written with a lease is
// attached to it, and revoking a lease deletes its keys. It serves both the
// lease verbs and the MVCC PutWithLease that attaches a key.
type sharedLessor struct {
	service.MVCCKVServicer
	mu     sync.Mutex
	next   int64
	leases map[int64]*lease.Lease
	kv     map[string]int64
}

var (
	_ service.LeaseServicer  = (*sharedLessor)(nil)
	_ service.MVCCKVServicer = (*sharedLessor)(nil)
)

func newSharedLessor() *sharedLessor {
	return &sharedLessor{leases: map[int64]*lease.Lease{}, kv: map[string]int64{}}
}

func (l *sharedLessor) copyOf(id int64) *lease.Lease {
	cp := *l.leases[id]
	cp.Keys = append([][]byte(nil), l.leases[id].Keys...)
	return &cp
}

func (l *sharedLessor) Grant(_ context.Context, ttl int64) (*lease.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	l.leases[l.next] = &lease.Lease{ID: l.next, TTL: ttl, GrantedTTL: ttl}
	return l.copyOf(l.next), nil
}

func (l *sharedLessor) PutWithLease(_ context.Context, key string, _ []byte, id int64) (*service.KeyValue, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	le, ok := l.leases[id]
	if !ok {
		return nil, &service.LeaseNotFoundError{LeaseID: id}
	}
	le.Keys = append(le.Keys, []byte(key))
	l.kv[key] = id
	return &service.KeyValue{Key: []byte(key)}, nil
}

func (l *sharedLessor) Revoke(_ context.Context, id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	le, ok := l.leases[id]
	if !ok {
		return &service.LeaseNotFoundError{LeaseID: id}
	}
	for _, k := range le.Keys {
		delete(l.kv, string(k))
	}
	delete(l.leases, id)
	return nil
}

func (l *sharedLessor) KeepAlive(_ context.Context, id int64) (*lease.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.leases[id]; !ok {
		return nil, &service.LeaseNotFoundError{LeaseID: id}
	}
	return l.copyOf(id), nil
}

func (l *sharedLessor) TimeToLive(ctx context.Context, id int64) (*lease.Lease, error) {
	return l.KeepAlive(ctx, id)
}

func (l *sharedLessor) Leases(_ context.Context) ([]*lease.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*lease.Lease, 0, len(l.leases))
	for id := range l.leases {
		out = append(out, l.copyOf(id))
	}
	return out, nil
}

// TestNamespacedLease_ConfinedToItsNamespace pins that a lease belongs to
// the (tenant, database) that granted it. Lease IDs are global, and the lease
// view used to operate on the global lease set, so any tenant could list
// every tenant's leases and keep alive, inspect or revoke them -- over REST,
// gRPC and, since the etcd listener resolves through the registry, etcd.
func TestNamespacedLease_ConfinedToItsNamespace(t *testing.T) {
	lessor := newSharedLessor()
	acme := newNamespacedLeaseService(lessor, lessor, "acme/app/")
	globex := newNamespacedLeaseService(lessor, lessor, "globex/app/")
	ctx := context.Background()

	granted, err := acme.Grant(ctx, 30)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	id := granted.ID

	var notFound *service.LeaseNotFoundError
	// Revoking a missing lease is an idempotent success, so a foreign one
	// answers the same -- and must not actually revoke it.
	if err := globex.Revoke(ctx, id); err != nil {
		t.Errorf("another tenant's Revoke = %v, want the no-op success a missing lease gets", err)
	}
	if err := globex.Revoke(ctx, id+1000); err != nil {
		t.Errorf("Revoke of a missing lease = %v, want idempotent success", err)
	}
	if _, err := globex.KeepAlive(ctx, id); !errors.As(err, &notFound) {
		t.Errorf("another tenant's KeepAlive = %v, want *service.LeaseNotFoundError", err)
	}
	if _, err := globex.TimeToLive(ctx, id); !errors.As(err, &notFound) {
		t.Errorf("another tenant's TimeToLive = %v, want *service.LeaseNotFoundError", err)
	}
	if list, err := globex.Leases(ctx); err != nil || len(list) != 0 {
		t.Errorf("another tenant's Leases = %v, %v; want none", list, err)
	}

	// The owner still has it, with no internal key in view.
	if _, err := lessor.TimeToLive(ctx, id); err != nil {
		t.Fatalf("the lease is gone after another tenant's attempts: %v", err)
	}
	if list, err := acme.Leases(ctx); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("owner Leases = %v, %v; want its one lease", list, err)
	}
	ttl, err := acme.TimeToLive(ctx, id)
	if err != nil || len(ttl.Keys) != 0 {
		t.Fatalf("owner TimeToLive = %+v, %v; want the lease with no visible keys", ttl, err)
	}
	if _, err := acme.KeepAlive(ctx, id); err != nil {
		t.Fatalf("owner KeepAlive: %v", err)
	}
	if err := acme.Revoke(ctx, id); err != nil {
		t.Fatalf("owner Revoke: %v", err)
	}
	if len(lessor.kv) != 0 {
		t.Fatalf("revoking left the ownership record: %v", lessor.kv)
	}
}

// A lease of the same ID in a different database of the same tenant is not
// this database's either, and a lease nobody recorded (granted outside any
// namespaced view) belongs to no namespace.
func TestNamespacedLease_OtherDatabaseAndUnrecordedLeases(t *testing.T) {
	lessor := newSharedLessor()
	app := newNamespacedLeaseService(lessor, lessor, "acme/app/")
	billing := newNamespacedLeaseService(lessor, lessor, "acme/billing/")
	ctx := context.Background()

	granted, err := app.Grant(ctx, 10)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	var notFound *service.LeaseNotFoundError
	if _, err := billing.TimeToLive(ctx, granted.ID); !errors.As(err, &notFound) {
		t.Errorf("another database's TimeToLive = %v, want not found", err)
	}
	raw, _ := lessor.Grant(ctx, 10)
	if _, err := app.TimeToLive(ctx, raw.ID); !errors.As(err, &notFound) {
		t.Errorf("an unrecorded lease's TimeToLive = %v, want not found", err)
	}
	// Keys the owner attaches are presented stripped.
	if _, err := lessor.PutWithLease(ctx, "acme/app/k1", nil, granted.ID); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	ka, err := app.KeepAlive(ctx, granted.ID)
	if err != nil || len(ka.Keys) != 1 || !bytes.Equal(ka.Keys[0], []byte("k1")) {
		t.Fatalf("owner KeepAlive keys = %q, %v; want [k1]", ka.Keys, err)
	}
}

// failingOwnerRecord refuses the ownership write.
type failingOwnerRecord struct {
	*sharedLessor
}

func (f failingOwnerRecord) PutWithLease(context.Context, string, []byte, int64) (*service.KeyValue, error) {
	return nil, errBoom
}

// A lease whose ownership cannot be recorded is not handed out: it is revoked
// and the failure returned, so no lease exists that no namespace owns.
func TestNamespacedLease_GrantRevokesWhenOwnershipIsNotRecorded(t *testing.T) {
	lessor := newSharedLessor()
	w := newNamespacedLeaseService(lessor, failingOwnerRecord{lessor}, "acme/app/")
	if _, err := w.Grant(context.Background(), 5); !errors.Is(err, errBoom) {
		t.Fatalf("Grant = %v, want the recording failure", err)
	}
	if len(lessor.leases) != 0 {
		t.Fatalf("an unowned lease survived: %v", lessor.leases)
	}
}

func TestNamespacedLease_NoMVCCRefusesGrant(t *testing.T) {
	w := newNamespacedLeaseService(newSharedLessor(), nil, "acme/app/")
	var unsupported *service.CapabilityUnsupportedError
	if _, err := w.Grant(context.Background(), 5); !errors.As(err, &unsupported) {
		t.Fatalf("Grant without an MVCC engine = %v, want *service.CapabilityUnsupportedError", err)
	}
}
