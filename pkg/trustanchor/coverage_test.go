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

package trustanchor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// faultingKVStore wraps a real MemoryKVStore, delegating every call to it
// except the ones a test arms to fail. It exists to drive the store-failure
// branches of KVRegistry that kvstore.NewMemoryKVStore itself never takes,
// and -- since KVRegistry.Add and .Remove now apply through
// kvstore.AtomicKVStore (go-qrdb security review) -- implements that
// interface too, so a test can arm a failure at the read (Get) or the
// atomic write (PutIfAbsent, CompareAndSwap) independently. Errors can be
// armed AFTER construction, so a test can let an Add succeed and then arm a
// failure for the Remove that follows.
type faultingKVStore struct {
	inner *kvstore.MemoryKVStore

	getErr         error
	putIfAbsentErr error
	casErr         error
}

func (s *faultingKVStore) Put(ctx context.Context, key string, value []byte) error {
	return s.inner.Put(ctx, key, value)
}

func (s *faultingKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.inner.Get(ctx, key)
}

func (s *faultingKVStore) Delete(ctx context.Context, key string) error {
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

func (s *faultingKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.putIfAbsentErr != nil {
		return false, s.putIfAbsentErr
	}
	return s.inner.PutIfAbsent(ctx, key, value)
}

func (s *faultingKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.inner.CompareAndDelete(ctx, key, expected)
}

func (s *faultingKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.casErr != nil {
		return false, s.casErr
	}
	return s.inner.CompareAndSwap(ctx, key, expected, next)
}

// compile-time interface checks.
var (
	_ kvstore.KVStore       = (*faultingKVStore)(nil)
	_ kvstore.AtomicKVStore = (*faultingKVStore)(nil)
)

// erroringSource is a Source whose CABundle always fails. It exists to
// drive tenantAnchors' propagated-error branch at each of its call sites
// (ChainContainsAnchor, VerifyClientCertificate) independently of
// KVRegistry, which never surfaces a bare store error from CABundle itself.
type erroringSource struct {
	err error
}

func (s erroringSource) CABundle(ctx context.Context, tenantID string) ([]byte, error) {
	return nil, s.err
}

func TestWithClock_SetsTheClock(t *testing.T) {
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	r := NewKVRegistry(kvstore.NewMemoryKVStore(), WithClock(func() time.Time { return fixed }))
	if got := r.now(); !got.Equal(fixed) {
		t.Fatalf("r.now() = %v, want %v", got, fixed)
	}
}

func TestWithClock_NilLeavesTheDefault(t *testing.T) {
	r := NewKVRegistry(kvstore.NewMemoryKVStore(), WithClock(nil))
	if r.now == nil {
		t.Fatal("WithClock(nil) cleared the default clock")
	}
	if r.now().IsZero() {
		t.Fatal("the default clock returned the zero time")
	}
}

func TestScoped_NilStoreReportsAStoreError(t *testing.T) {
	ctx := context.Background()
	r := &KVRegistry{now: time.Now}

	var se *StoreError
	if _, err := r.CABundle(ctx, "acme"); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("CABundle over a nil store = %v, want *StoreError{Op: scope}", err)
	}
	if _, err := r.List(ctx, "acme"); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("List over a nil store = %v, want *StoreError{Op: scope}", err)
	}
	if err := r.Remove(ctx, "acme", "fp"); !errors.As(err, &se) || se.Op != "scope" {
		t.Fatalf("Remove over a nil store = %v, want *StoreError{Op: scope}", err)
	}
}

func TestScoped_SystemTenantBypassesShapeValidation(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newRoot(t, "sys-root")

	for _, id := range []string{tenantid.System, ""} {
		if _, err := r.Add(ctx, id, EncodePEM(root.cert)); err != nil {
			t.Fatalf("Add for tenant %q = %v, want the system tenant accepted without shape validation", id, err)
		}
	}
	list, err := r.List(ctx, tenantid.System)
	if err != nil || len(list) != 1 {
		t.Fatalf("List(system) = %v, %v; want the one anchor added under both spellings", list, err)
	}
}

func TestList_RejectsABadTenantIDBeforeReadingAnything(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	var bad *InvalidTenantError
	if _, err := r.List(ctx, "acme/evil"); !errors.As(err, &bad) {
		t.Fatalf("List for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
}

func TestCABundle_RejectsABadTenantIDBeforeReadingAnything(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	var bad *InvalidTenantError
	if _, err := r.CABundle(ctx, "acme/evil"); !errors.As(err, &bad) {
		t.Fatalf("CABundle for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
}

func TestRemove_RejectsABadTenantIDBeforeReadingAnything(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	var bad *InvalidTenantError
	if err := r.Remove(ctx, "acme/evil", "fp"); !errors.As(err, &bad) {
		t.Fatalf("Remove for a nesting tenant id = %v, want *InvalidTenantError", err)
	}
}

func TestRemove_RejectsAnInvalidFingerprintWithoutTouchingTheStore(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore())
	var nf *AnchorNotFoundError
	if err := r.Remove(ctx, "acme", ""); !errors.As(err, &nf) {
		t.Fatalf("Remove with an empty fingerprint = %v, want *AnchorNotFoundError", err)
	}
	if err := r.Remove(ctx, "acme", "ab/cd"); !errors.As(err, &nf) {
		t.Fatalf("Remove with a path-shaped fingerprint = %v, want *AnchorNotFoundError", err)
	}
}

func TestCABundle_PropagatesAGetFailure(t *testing.T) {
	ctx := context.Background()
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), getErr: errors.New("get boom")}
	r := NewKVRegistry(fs)

	var se *StoreError
	if _, err := r.CABundle(ctx, "acme"); !errors.As(err, &se) || se.Op != "get" {
		t.Fatalf("CABundle over a failing Get = %v, want *StoreError{Op: get}", err)
	}
}

func TestList_PropagatesAGetFailure(t *testing.T) {
	ctx := context.Background()
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), getErr: errors.New("get boom")}
	r := NewKVRegistry(fs)

	var se *StoreError
	if _, err := r.List(ctx, "acme"); !errors.As(err, &se) || se.Op != "get" {
		t.Fatalf("List over a failing Get = %v, want *StoreError{Op: get}", err)
	}
}

func TestAdd_PropagatesAGetFailure(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t, "r")
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), getErr: errors.New("get boom")}
	r := NewKVRegistry(fs)

	var se *StoreError
	if _, err := r.Add(ctx, "acme", EncodePEM(root.cert)); !errors.As(err, &se) || se.Op != "get" {
		t.Fatalf("Add over a failing Get = %v, want *StoreError{Op: get}", err)
	}
}

func TestAdd_PropagatesAPutFailure(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t, "r")
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), putIfAbsentErr: errors.New("put boom")}
	r := NewKVRegistry(fs)

	var se *StoreError
	if _, err := r.Add(ctx, "acme", EncodePEM(root.cert)); !errors.As(err, &se) || se.Op != "put" {
		t.Fatalf("Add over a failing PutIfAbsent = %v, want *StoreError{Op: put}", err)
	}
}

func TestRemove_PropagatesAGetFailure(t *testing.T) {
	ctx := context.Background()
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore(), getErr: errors.New("get boom")}
	r := NewKVRegistry(fs)

	var se *StoreError
	if err := r.Remove(ctx, "acme", "deadbeef"); !errors.As(err, &se) || se.Op != "get" {
		t.Fatalf("Remove over a failing Get = %v, want *StoreError{Op: get}", err)
	}
}

func TestRemove_PropagatesACompareAndSwapFailure(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t, "r")
	fs := &faultingKVStore{inner: kvstore.NewMemoryKVStore()}
	r := NewKVRegistry(fs)
	if _, err := r.Add(ctx, "acme", EncodePEM(root.cert)); err != nil {
		t.Fatal(err)
	}
	fs.casErr = errors.New("cas boom")

	var se *StoreError
	if err := r.Remove(ctx, "acme", Fingerprint(root.cert)); !errors.As(err, &se) || se.Op != "put" {
		t.Fatalf("Remove over a failing CompareAndSwap = %v, want *StoreError{Op: put}", err)
	}
}

// corrupt writes raw bytes directly to tenantID's whole-set record, bypassing
// Add's validation entirely -- simulating a record damaged after it was
// written.
func corrupt(t *testing.T, store kvstore.KVStore, tenantID string, value []byte) {
	t.Helper()
	s, err := kvstore.NewNamespacedKVStore(store, tenant.IdentityNamespace(tenantID))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), anchorSetKey, value); err != nil {
		t.Fatal(err)
	}
}

func TestReadSet_PropagatesACorruptJSONRecord(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	corrupt(t, store, "acme", []byte("not json"))
	r := NewKVRegistry(store)

	var se *StoreError
	if _, err := r.CABundle(ctx, "acme"); !errors.As(err, &se) || se.Op != "decode" {
		t.Fatalf("CABundle over a corrupt record = %v, want *StoreError{Op: decode}", err)
	}
	if _, err := r.List(ctx, "acme"); !errors.As(err, &se) || se.Op != "decode" {
		t.Fatalf("List over a corrupt record = %v, want *StoreError{Op: decode}", err)
	}
}

func TestList_PropagatesAnUndecodableStoredCertificate(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	set := anchorSet{Anchors: map[string]storedAnchor{
		"deadbeef": {CertPEM: []byte("not a certificate"), AddedAt: time.Now().UTC()},
	}}
	value, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	corrupt(t, store, "acme", value)
	r := NewKVRegistry(store)

	var se *StoreError
	if _, err := r.List(ctx, "acme"); !errors.As(err, &se) || se.Op != "decode" {
		t.Fatalf("List over an undecodable cert = %v, want *StoreError{Op: decode}", err)
	}
}

func TestParseCertificates_SkipsNonCertificatePEMBlocks(t *testing.T) {
	root := newRoot(t, "r")
	bundle := append([]byte("-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"), EncodePEM(root.cert)...)
	certs, err := ParseCertificates(bundle)
	if err != nil || len(certs) != 1 {
		t.Fatalf("ParseCertificates with a leading non-certificate block = %v, %v; want the certificate alone", certs, err)
	}
}

func TestParseCertificates_EmptyInputIsAnEmptyBundle(t *testing.T) {
	var empty *EmptyBundleError
	if _, err := ParseCertificates(nil); !errors.As(err, &empty) {
		t.Fatalf("ParseCertificates(nil) = %v, want *EmptyBundleError", err)
	}
}

func TestChainContainsAnchor_PropagatesASourceFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("source boom")
	root := newRoot(t, "r")
	issuing := root.intermediate(t, "issuing")
	leaf := issuing.clientLeaf(t, "alice", "acme")
	chain := []*x509.Certificate{leaf, issuing.cert}

	if _, err := ChainContainsAnchor(ctx, erroringSource{err: boom}, "acme", chain); !errors.Is(err, boom) {
		t.Fatalf("ChainContainsAnchor over a failing Source = %v, want %v", err, boom)
	}
}

func TestVerifyClientCertificate_PropagatesASourceFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("source boom")
	root := newRoot(t, "r")
	leaf := root.clientLeaf(t, "alice", "acme")

	if err := VerifyClientCertificate(ctx, erroringSource{err: boom}, "acme", leaf, nil, time.Now()); !errors.Is(err, boom) {
		t.Fatalf("VerifyClientCertificate over a failing Source = %v, want %v", err, boom)
	}
}

func TestChanges_NotifiesEverySubscriberInOrder(t *testing.T) {
	var c Changes
	var order []int
	c.Subscribe(func() { order = append(order, 1) })
	c.Subscribe(func() { order = append(order, 2) })
	c.Subscribe(nil) // ignored.

	c.Notify()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("Notify order = %v, want [1 2]", order)
	}

	c.Notify()
	if len(order) != 4 {
		t.Fatalf("a second Notify ran %d times total, want 4", len(order))
	}
}

func TestChanges_NotifyWithNoSubscribersIsANoOp(t *testing.T) {
	var c Changes
	c.Notify() // must not panic.
}
