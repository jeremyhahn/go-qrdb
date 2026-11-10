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

package tenantadmin

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// faultingKVStore wraps a real KVStore, delegating every call to it except
// the ones a test arms to fail.
type faultingKVStore struct {
	inner kvstore.KVStore

	putErr    error
	getErr    error
	deleteErr error
}

func (s *faultingKVStore) Put(ctx context.Context, key string, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.inner.Put(ctx, key, value)
}

// PutIfAbsent makes the double an AtomicKVStore, as the production store is;
// it fails with putErr when armed, else delegates to the real store.
func (s *faultingKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.putErr != nil {
		return false, s.putErr
	}
	return s.inner.(kvstore.AtomicKVStore).PutIfAbsent(ctx, key, value)
}

// CompareAndDelete and CompareAndSwap complete kvstore.AtomicKVStore.
func (s *faultingKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	return s.inner.(kvstore.AtomicKVStore).CompareAndDelete(ctx, key, expected)
}

func (s *faultingKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return s.inner.(kvstore.AtomicKVStore).CompareAndSwap(ctx, key, expected, next)
}

func (s *faultingKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.inner.Get(ctx, key)
}

func (s *faultingKVStore) Delete(ctx context.Context, key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.inner.Delete(ctx, key)
}

func (s *faultingKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return s.inner.Scan(ctx, prefix, fn)
}

func (s *faultingKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *faultingKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *faultingKVStore) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	return s.inner.RegisterEntityIndexes(entityType, indexes)
}

func (s *faultingKVStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	return s.inner.QueryIndex(ctx, indexName, value)
}

func (s *faultingKVStore) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	return s.inner.ScanIndex(ctx, indexName, start, end)
}

func (s *faultingKVStore) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	return s.inner.QueryCompoundIndex(ctx, indexName, values)
}

func (s *faultingKVStore) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	return s.inner.ScanCompoundIndex(ctx, indexName, prefixValues, rangeStart, rangeEnd)
}

var _ kvstore.KVStore = (*faultingKVStore)(nil)

func TestKVStore_RoundTrips(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	p := &Pending{Username: "alice", Thumbprint: "abc123", NotAfter: time.Now().Add(time.Hour).UTC(), Serial: "1"}

	if err := s.Create(ctx, "acme", p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Username != p.Username || got.Thumbprint != p.Thumbprint || got.Serial != p.Serial || !got.NotAfter.Equal(p.NotAfter) {
		t.Fatalf("Get = %+v, want %+v", got, p)
	}

	if deleted, err := s.Delete(ctx, "acme", got); err != nil || !deleted {
		t.Fatalf("Delete of the record Get read = %v, %v; want true, nil", deleted, err)
	}
	got, err = s.Get(ctx, "acme")
	if err != nil || got != nil {
		t.Fatalf("Get after Delete = %v, %v; want nil, nil", got, err)
	}
}

func TestSOKVStore_RoundTripsUnderThePendingSOKey(t *testing.T) {
	ctx := context.Background()
	backing := kvstore.NewMemoryKVStore()
	s := NewSOKVStore(backing)
	p := &Pending{Username: "sec", Thumbprint: "def456", NotAfter: time.Now().Add(time.Hour).UTC()}
	if err := s.Create(ctx, "acme", p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	scoped, err := kvstore.NewNamespacedKVStore(backing, tenant.IdentityNamespace("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := scoped.Get(ctx, pendingSOKey); err != nil || raw == nil {
		t.Fatalf("record under %s = %q, %v; want it present", pendingSOKey, raw, err)
	}
	got, err := s.Get(ctx, "acme")
	if err != nil || got == nil || got.Username != "sec" || got.Thumbprint != "def456" {
		t.Fatalf("Get = %+v, %v; want the recorded SO", got, err)
	}
	if deleted, err := s.Delete(ctx, "acme", got); err != nil || !deleted {
		t.Fatalf("Delete = %v, %v; want true, nil", deleted, err)
	}
}

func TestSOKVStore_IsIndependentOfThePendingAdmin(t *testing.T) {
	ctx := context.Background()
	backing := kvstore.NewMemoryKVStore()
	admins, sos := NewKVStore(backing), NewSOKVStore(backing)
	if err := admins.Create(ctx, "acme", &Pending{Username: "admin", Thumbprint: "a"}); err != nil {
		t.Fatalf("admin Create: %v", err)
	}
	if got, err := sos.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("SO Get with only an admin pending = %+v, %v; want nil, nil", got, err)
	}
	if err := sos.Create(ctx, "acme", &Pending{Username: "sec", Thumbprint: "s"}); err != nil {
		t.Fatalf("SO Create beside a pending admin: %v", err)
	}
	if got, err := admins.Get(ctx, "acme"); err != nil || got == nil || got.Username != "admin" {
		t.Fatalf("admin Get = %+v, %v; want the admin untouched", got, err)
	}
}

func TestKVStore_GetOfAnAbsentRecordIsNilNil(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	got, err := s.Get(ctx, "acme")
	if err != nil || got != nil {
		t.Fatalf("Get of an absent record = %v, %v; want nil, nil", got, err)
	}
}

func TestKVStore_IsolatesTenants(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	acme := &Pending{Username: "alice", Thumbprint: "acme-thumb"}
	globex := &Pending{Username: "bob", Thumbprint: "globex-thumb"}

	if err := s.Create(ctx, "acme", acme); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "globex", globex); err != nil {
		t.Fatal(err)
	}

	gotAcme, err := s.Get(ctx, "acme")
	if err != nil || gotAcme.Username != "alice" {
		t.Fatalf("Get(acme) = %v, %v; want alice", gotAcme, err)
	}
	gotGlobex, err := s.Get(ctx, "globex")
	if err != nil || gotGlobex.Username != "bob" {
		t.Fatalf("Get(globex) = %v, %v; want bob", gotGlobex, err)
	}

	if deleted, err := s.Delete(ctx, "acme", globex); err != nil || deleted {
		t.Fatalf("Delete of a record acme does not hold = %v, %v; want false, nil", deleted, err)
	}
	// The record Create wrote, held in memory, is compared as it was encoded.
	if deleted, err := s.Delete(ctx, "acme", acme); err != nil || !deleted {
		t.Fatalf("Delete(acme) = %v, %v; want true, nil", deleted, err)
	}
	if got, err := s.Get(ctx, "globex"); err != nil || got == nil {
		t.Fatalf("deleting acme's record removed globex's: %v, %v", got, err)
	}
}

func TestKVStore_DeleteOfAnAbsentRecordIsNotAnError(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	if deleted, err := s.Delete(ctx, "acme", &Pending{Username: "alice", Thumbprint: "tp"}); err != nil || deleted {
		t.Fatalf("Delete of an absent record = %v, %v; want false, nil", deleted, err)
	}
}

func TestKVStore_RefusesTheSystemTenant(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	p := &Pending{Username: "alice", Thumbprint: "abc"}

	var bad *InvalidTenantError
	if err := s.Create(ctx, tenantid.System, p); !errors.As(err, &bad) {
		t.Fatalf("Put for the system tenant = %v, want *InvalidTenantError", err)
	}
	if _, err := s.Get(ctx, tenantid.System); !errors.As(err, &bad) {
		t.Fatalf("Get for the system tenant = %v, want *InvalidTenantError", err)
	}
	if _, err := s.Delete(ctx, tenantid.System, &Pending{Username: "alice", Thumbprint: "tp"}); !errors.As(err, &bad) {
		t.Fatalf("Delete for the system tenant = %v, want *InvalidTenantError", err)
	}
	// The empty tenant id is the same tenant as SystemTenantID.
	if err := s.Create(ctx, "", p); !errors.As(err, &bad) {
		t.Fatalf("Put for the empty tenant id = %v, want *InvalidTenantError", err)
	}
}

func TestKVStore_RefusesAnInvalidTenantID(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	p := &Pending{Username: "alice", Thumbprint: "abc"}

	var bad *InvalidTenantError
	if err := s.Create(ctx, "acme/evil", p); !errors.As(err, &bad) || bad.Unwrap() == nil {
		t.Fatalf("Put for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
	if _, err := s.Get(ctx, "acme/evil"); !errors.As(err, &bad) {
		t.Fatalf("Get for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
	if _, err := s.Delete(ctx, "acme/evil", &Pending{Username: "alice", Thumbprint: "tp"}); !errors.As(err, &bad) {
		t.Fatalf("Delete for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
}

func TestKVStore_RefusesAnIncompleteRecord(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())

	cases := map[string]*Pending{
		"nil":             nil,
		"no username":     {Thumbprint: "abc"},
		"no thumbprint":   {Username: "alice"},
		"both empty":      {},
		"username only":   {Username: "alice", Thumbprint: ""},
		"thumbprint only": {Username: "", Thumbprint: "abc"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			var incomplete *IncompleteError
			if err := s.Create(ctx, "acme", p); !errors.As(err, &incomplete) {
				t.Fatalf("Put(%v) = %v, want *IncompleteError", p, err)
			}
		})
	}
}

func TestKVStore_PropagatesAPutFailure(t *testing.T) {
	ctx := context.Background()
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), putErr: errors.New("put boom")}
	s := NewKVStore(fs)

	var se *StoreError
	if err := s.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "abc"}); !errors.As(err, &se) || se.Op != "create" {
		t.Fatalf("Create over a failing store = %v, want *StoreError{Op: create}", err)
	}
}

func TestKVStore_PropagatesAGetFailure(t *testing.T) {
	ctx := context.Background()
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), getErr: errors.New("get boom")}
	s := NewKVStore(fs)

	var se *StoreError
	if _, err := s.Get(ctx, "acme"); !errors.As(err, &se) || se.Op != "get" {
		t.Fatalf("Get over a failing store = %v, want *StoreError{Op: get}", err)
	}
}

func TestKVStore_PropagatesADeleteFailure(t *testing.T) {
	ctx := context.Background()
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), deleteErr: errors.New("delete boom")}
	s := NewKVStore(fs)

	var se *StoreError
	if _, err := s.Delete(ctx, "acme", &Pending{Username: "alice", Thumbprint: "tp"}); !errors.As(err, &se) || se.Op != "delete" {
		t.Fatalf("Delete over a failing store = %v, want *StoreError{Op: delete}", err)
	}
}

// TestKVStore_DeleteLeavesARecordRecordedSinceTheRead is the guard: a caller
// that read a record which was completed and replaced before its delete must
// not remove the replacement it never saw.
func TestKVStore_DeleteLeavesARecordRecordedSinceTheRead(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	if err := s.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "first"}); err != nil {
		t.Fatal(err)
	}
	stale, err := s.Get(ctx, "acme")
	if err != nil || stale == nil {
		t.Fatalf("Get = %v, %v", stale, err)
	}
	if deleted, err := s.Delete(ctx, "acme", stale); err != nil || !deleted {
		t.Fatalf("first Delete = %v, %v; want true, nil", deleted, err)
	}
	if err := s.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "second"}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.Delete(ctx, "acme", stale); err != nil || deleted {
		t.Fatalf("Delete with the stale read = %v, %v; want false, nil", deleted, err)
	}
	if got, err := s.Get(ctx, "acme"); err != nil || got == nil || got.Thumbprint != "second" {
		t.Fatalf("Get after the stale Delete = %+v, %v; want the second record", got, err)
	}
}

func TestKVStore_DeleteRefusesANilRead(t *testing.T) {
	s := NewKVStore(kvstore.NewMemoryKVStore())
	var inc *IncompleteError
	if _, err := s.Delete(context.Background(), "acme", nil); !errors.As(err, &inc) {
		t.Fatalf("Delete(nil) = %v, want *IncompleteError", err)
	}
}

func TestKVStore_DeleteRefusesANonAtomicStore(t *testing.T) {
	s := NewKVStore(nonAtomicKVStore{kvstore.NewMemoryKVStore()})
	var se *StoreError
	_, err := s.Delete(context.Background(), "acme", &Pending{Username: "alice", Thumbprint: "t"})
	if !errors.As(err, &se) || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("Delete over a non-atomic store = %v, want a StoreError wrapping ErrAtomicWriteUnsupported", err)
	}
}

// unencodablePending is a record encoding/json refuses: time.Time marshals
// only years 0 through 9999 (RFC 3339).
func unencodablePending() *Pending {
	return &Pending{Username: "alice", Thumbprint: "tp", NotAfter: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func TestKVStore_CreateRefusesAnUnencodableRecord(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	var se *StoreError
	if err := s.Create(ctx, "acme", unencodablePending()); !errors.As(err, &se) || se.Op != "encode" {
		t.Fatalf("Create of an unencodable record = %v, want *StoreError{Op: encode}", err)
	}
	if got, err := s.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("Get after a refused Create = %+v, %v; want nil, nil", got, err)
	}
}

// TestKVStore_DeleteRefusesAnUnencodableInMemoryRead covers the record built
// in memory, which Delete encodes to compare: one that cannot be encoded is
// refused and the stored record is left in place.
func TestKVStore_DeleteRefusesAnUnencodableInMemoryRead(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	if err := s.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "tp"}); err != nil {
		t.Fatal(err)
	}
	var se *StoreError
	deleted, err := s.Delete(ctx, "acme", unencodablePending())
	if !errors.As(err, &se) || se.Op != "encode" || deleted {
		t.Fatalf("Delete with an unencodable read = %v, %v; want false, *StoreError{Op: encode}", deleted, err)
	}
	if got, err := s.Get(ctx, "acme"); err != nil || got == nil || got.Thumbprint != "tp" {
		t.Fatalf("Get after a refused Delete = %+v, %v; want the stored record", got, err)
	}
}

func TestKVStore_PropagatesAScopeFailure(t *testing.T) {
	ctx := context.Background()
	s := &KVStore{} // nil inner store: scoping fails at NewNamespacedKVStore.

	var se *StoreError
	if err := s.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "abc"}); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Put over a nil store = %v, want *StoreError{Op: scope}", err)
	}
	if _, err := s.Get(ctx, "acme"); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Get over a nil store = %v, want *StoreError{Op: scope}", err)
	}
	if _, err := s.Delete(ctx, "acme", &Pending{Username: "alice", Thumbprint: "tp"}); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Delete over a nil store = %v, want *StoreError{Op: scope}", err)
	}
}

func TestKVStore_PropagatesACorruptStoredRecord(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	scoped, err := kvstore.NewNamespacedKVStore(store, tenant.IdentityNamespace("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.Put(ctx, pendingKey, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	s := NewKVStore(store)

	var se *StoreError
	if _, err := s.Get(ctx, "acme"); !errors.As(err, &se) || se.Op != "decode" {
		t.Fatalf("Get over a corrupt record = %v, want *StoreError{Op: decode}", err)
	}
}

func TestErrors_DescribeAndUnwrap(t *testing.T) {
	cause := errors.New("cause")

	withCause := &InvalidTenantError{TenantID: "acme/evil", Err: cause}
	if withCause.Error() == "" || !errors.Is(withCause, cause) {
		t.Fatal("InvalidTenantError with a cause does not describe or unwrap it")
	}
	withoutCause := &InvalidTenantError{TenantID: "system"}
	if withoutCause.Error() == "" || withoutCause.Unwrap() != nil {
		t.Fatal("InvalidTenantError without a cause misbehaves")
	}

	if (&IncompleteError{}).Error() == "" {
		t.Fatal("IncompleteError has empty text")
	}

	se := &StoreError{Op: "put", Err: cause}
	if se.Error() == "" || !errors.Is(se, cause) {
		t.Fatal("StoreError does not describe or unwrap its cause")
	}

	already := &AlreadyPendingError{TenantID: "acme"}
	if already.Error() == "" {
		t.Fatal("AlreadyPendingError has empty text")
	}
}

// TestKVStore_CreateNeverReplacesARecord proves the pending record is
// create-if-absent: a second Create for the same tenant is refused and the
// first record survives.
func TestKVStore_CreateNeverReplacesARecord(t *testing.T) {
	ctx := context.Background()
	s := NewKVStore(kvstore.NewMemoryKVStore())
	if err := s.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "first"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	var already *AlreadyPendingError
	if err := s.Create(ctx, "acme", &Pending{Username: "mallory", Thumbprint: "second"}); !errors.As(err, &already) {
		t.Fatalf("second Create = %v, want *AlreadyPendingError", err)
	}
	got, err := s.Get(ctx, "acme")
	if err != nil || got.Username != "alice" || got.Thumbprint != "first" {
		t.Fatalf("record after a refused Create = %+v, %v; want the first", got, err)
	}
	if already.Error() == "" {
		t.Fatal("empty error message")
	}
}

// TestKVStore_CreateRefusesANonAtomicStore proves Create will not fall back
// to a check-then-put two writers could both pass.
func TestKVStore_CreateRefusesANonAtomicStore(t *testing.T) {
	s := NewKVStore(nonAtomicKVStore{kvstore.NewMemoryKVStore()})
	var se *StoreError
	if err := s.Create(context.Background(), "acme", &Pending{Username: "alice", Thumbprint: "t"}); !errors.As(err, &se) || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("Create over a non-atomic store = %v, want a StoreError wrapping ErrAtomicWriteUnsupported", err)
	}
}

// TestAuditKVStore_RoundTripsAndDeletesTheReadRecord: a kept audit record
// reads back byte for byte, a Delete of exactly what was read removes it, and
// the tenant then has none.
func TestAuditKVStore_RoundTripsAndDeletesTheReadRecord(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(kvstore.NewMemoryKVStore())
	record := []byte(`{"action":"tenant.onboard"}`)

	if err := s.Create(ctx, "acme", record); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "acme")
	if err != nil || !bytes.Equal(got, record) {
		t.Fatalf("Get = %q, %v; want %q", got, err, record)
	}
	deleted, err := s.Delete(ctx, "acme", got)
	if err != nil || !deleted {
		t.Fatalf("Delete of the read record = %v, %v; want true, nil", deleted, err)
	}
	if got, err := s.Get(ctx, "acme"); err != nil || got != nil {
		t.Fatalf("Get after Delete = %q, %v; want nil, nil", got, err)
	}
}

// TestAuditKVStore_GetOfAnAbsentRecordIsNilNil: a tenant with no kept record
// reads as none, not as an error.
func TestAuditKVStore_GetOfAnAbsentRecordIsNilNil(t *testing.T) {
	got, err := NewAuditKVStore(kvstore.NewMemoryKVStore()).Get(context.Background(), "acme")
	if err != nil || got != nil {
		t.Fatalf("Get of an absent record = %q, %v; want nil, nil", got, err)
	}
}

// TestAuditKVStore_CreateNeverReplacesARecord: a second record for the same
// tenant is refused and the first is kept.
func TestAuditKVStore_CreateNeverReplacesARecord(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(kvstore.NewMemoryKVStore())
	if err := s.Create(ctx, "acme", []byte("first")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var already *AlreadyPendingError
	if err := s.Create(ctx, "acme", []byte("second")); !errors.As(err, &already) || already.TenantID != "acme" {
		t.Fatalf("second Create = %v, want *AlreadyPendingError{acme}", err)
	}
	if got, _ := s.Get(ctx, "acme"); string(got) != "first" {
		t.Fatalf("record after a refused Create = %q, want first", got)
	}
}

// TestAuditKVStore_IsIndependentOfThePendingAdmin: the kept audit record and
// the pending admin sit under different keys of one tenant's namespace, so
// consuming one leaves the other.
func TestAuditKVStore_IsIndependentOfThePendingAdmin(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	admins, audits := NewKVStore(store), NewAuditKVStore(store)
	if err := admins.Create(ctx, "acme", &Pending{Username: "alice", Thumbprint: "abc"}); err != nil {
		t.Fatalf("pending admin Create: %v", err)
	}
	if err := audits.Create(ctx, "acme", []byte("record")); err != nil {
		t.Fatalf("audit Create: %v", err)
	}
	if deleted, err := audits.Delete(ctx, "acme", []byte("record")); err != nil || !deleted {
		t.Fatalf("audit Delete = %v, %v; want true, nil", deleted, err)
	}
	if p, err := admins.Get(ctx, "acme"); err != nil || p == nil || p.Username != "alice" {
		t.Fatalf("pending admin after the audit record's delete = %+v, %v; want alice kept", p, err)
	}
}

// TestAuditKVStore_DeleteLeavesADifferentOrAbsentRecord: Delete removes only
// the record it was given; one that differs, or none at all, deletes nothing.
func TestAuditKVStore_DeleteLeavesADifferentOrAbsentRecord(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(kvstore.NewMemoryKVStore())

	if deleted, err := s.Delete(ctx, "acme", []byte("record")); err != nil || deleted {
		t.Fatalf("Delete with no record = %v, %v; want false, nil", deleted, err)
	}
	if err := s.Create(ctx, "acme", []byte("record")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if deleted, err := s.Delete(ctx, "acme", []byte("other")); err != nil || deleted {
		t.Fatalf("Delete of a different record = %v, %v; want false, nil", deleted, err)
	}
	if got, _ := s.Get(ctx, "acme"); string(got) != "record" {
		t.Fatalf("record after a non-matching Delete = %q, want it kept", got)
	}
}

// TestAuditKVStore_RefusesAnEmptyRecord: an empty record is neither kept nor
// used to delete one.
func TestAuditKVStore_RefusesAnEmptyRecord(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(kvstore.NewMemoryKVStore())
	if err := s.Create(ctx, "acme", []byte("record")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var incomplete *IncompleteError
	if err := s.Create(ctx, "other", nil); !errors.As(err, &incomplete) {
		t.Fatalf("Create of an empty record = %v, want *IncompleteError", err)
	}
	if got, _ := s.Get(ctx, "other"); got != nil {
		t.Fatalf("an empty record was kept: %q", got)
	}
	if deleted, err := s.Delete(ctx, "acme", nil); !errors.As(err, &incomplete) || deleted {
		t.Fatalf("Delete with an empty read = %v, %v; want false, *IncompleteError", deleted, err)
	}
	if got, _ := s.Get(ctx, "acme"); string(got) != "record" {
		t.Fatalf("record after an empty-read Delete = %q, want it kept", got)
	}
}

// TestAuditKVStore_RefusesAnInvalidTenantID: a tenant id that would nest
// namespaces is refused by every operation.
func TestAuditKVStore_RefusesAnInvalidTenantID(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(kvstore.NewMemoryKVStore())

	var bad *InvalidTenantError
	if err := s.Create(ctx, "acme/evil", []byte("record")); !errors.As(err, &bad) {
		t.Fatalf("Create for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
	if _, err := s.Get(ctx, "acme/evil"); !errors.As(err, &bad) {
		t.Fatalf("Get for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
	if _, err := s.Delete(ctx, "acme/evil", []byte("record")); !errors.As(err, &bad) {
		t.Fatalf("Delete for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
}

// TestAuditKVStore_RefusesANonAtomicStore: without atomic writes a kept
// record could be overwritten or deleted after a concurrent change, so both
// writes are refused.
func TestAuditKVStore_RefusesANonAtomicStore(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(nonAtomicKVStore{kvstore.NewMemoryKVStore()})

	var se *StoreError
	if err := s.Create(ctx, "acme", []byte("record")); !errors.As(err, &se) || se.Op != "create" || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("Create over a non-atomic store = %v, want *StoreError{Op: create} wrapping ErrAtomicWriteUnsupported", err)
	}
	if _, err := s.Delete(ctx, "acme", []byte("record")); !errors.As(err, &se) || se.Op != "delete" || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("Delete over a non-atomic store = %v, want *StoreError{Op: delete} wrapping ErrAtomicWriteUnsupported", err)
	}
}

// TestAuditKVStore_PropagatesStoreFailures: a failing write, read or delete
// is reported as a StoreError naming the operation and wrapping the cause.
func TestAuditKVStore_PropagatesStoreFailures(t *testing.T) {
	ctx := context.Background()
	putErr, getErr, deleteErr := errors.New("put boom"), errors.New("get boom"), errors.New("delete boom")
	s := NewAuditKVStore(&faultingKVStore{inner: kvstore.NewMemoryKVStore(), putErr: putErr, getErr: getErr, deleteErr: deleteErr})

	var se *StoreError
	if err := s.Create(ctx, "acme", []byte("record")); !errors.As(err, &se) || se.Op != "create" || !errors.Is(err, putErr) {
		t.Fatalf("Create over a failing store = %v, want *StoreError{Op: create} wrapping %v", err, putErr)
	}
	if _, err := s.Get(ctx, "acme"); !errors.As(err, &se) || se.Op != "get" || !errors.Is(err, getErr) {
		t.Fatalf("Get over a failing store = %v, want *StoreError{Op: get} wrapping %v", err, getErr)
	}
	if _, err := s.Delete(ctx, "acme", []byte("record")); !errors.As(err, &se) || se.Op != "delete" || !errors.Is(err, deleteErr) {
		t.Fatalf("Delete over a failing store = %v, want *StoreError{Op: delete} wrapping %v", err, deleteErr)
	}
}

// TestAuditKVStore_PropagatesAScopeFailure: with no store to scope, every
// operation reports the scope failure.
func TestAuditKVStore_PropagatesAScopeFailure(t *testing.T) {
	ctx := context.Background()
	s := NewAuditKVStore(nil)

	var se *StoreError
	if err := s.Create(ctx, "acme", []byte("record")); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Create over a nil store = %v, want *StoreError{Op: scope}", err)
	}
	if _, err := s.Get(ctx, "acme"); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Get over a nil store = %v, want *StoreError{Op: scope}", err)
	}
	if _, err := s.Delete(ctx, "acme", []byte("record")); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Delete over a nil store = %v, want *StoreError{Op: scope}", err)
	}
}

// nonAtomicKVStore hides every capability but plain KVStore.
type nonAtomicKVStore struct{ kvstore.KVStore }
