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

package user

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// freshThumbprint is the SPKI thumbprint of a newly generated key.
func freshThumbprint(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return ComputeSPKIThumbprint(makeCertForKey(t, key, "invitee", 1))
}

func newActivationStore(t *testing.T) *kvUserStore {
	t.Helper()
	return &kvUserStore{store: newMemKVStore(), idgen: newTestIDGen(t)}
}

func TestActivateWithCertBinding_ActivatesAPendingAccountInOneCommit(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tp := freshThumbprint(t)
	notAfter := time.Now().Add(time.Hour).Unix()

	got, err := s.ActivateWithCertBinding(ctx, u.ID, tp, notAfter)
	if err != nil {
		t.Fatalf("ActivateWithCertBinding: %v", err)
	}
	if got.Lifecycle != LifecycleActive || !got.HasCertBinding(tp) {
		t.Fatalf("activated = %s with bindings %v; want active and bound", got.Lifecycle, got.CertBindings)
	}
	// The committed record, not just the returned one, carries both.
	stored, err := s.GetUser(ctx, u.ID)
	if err != nil || stored.Lifecycle != LifecycleActive || !stored.HasCertBinding(tp) {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if byKey, err := s.UserByCertThumbprint(tenant.WithTenantID(ctx, "acme"), tp); err != nil || byKey.ID != u.ID {
		t.Fatalf("UserByCertThumbprint = %v, %v", byKey, err)
	}
}

func TestActivateWithCertBinding_RefusesAnAccountThatIsNotPending(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	active, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "active", TenantID: "acme", WithoutEmailLoginKey: true})
	disabled, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "off", TenantID: "acme", WithoutEmailLoginKey: true, Lifecycle: LifecycleDisabled})

	for _, u := range []*User{active, disabled} {
		tp := freshThumbprint(t)
		_, err := s.ActivateWithCertBinding(ctx, u.ID, tp, 0)
		var np *AccountNotPendingError
		if !errors.As(err, &np) || np.ID != u.ID || np.HasCredential {
			t.Fatalf("%s: err = %v, want *AccountNotPendingError", u.Username, err)
		}
		stored, _ := s.GetUser(ctx, u.ID)
		if stored.HasCertBinding(tp) || stored.Lifecycle != u.Lifecycle {
			t.Fatalf("%s: a refused activation wrote: %+v", u.Username, stored)
		}
	}
}

func TestActivateWithCertBinding_RefusesAPendingAccountThatHoldsABinding(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	if _, err := s.AddCertBinding(ctx, u.ID, freshThumbprint(t), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	_, err := s.ActivateWithCertBinding(ctx, u.ID, freshThumbprint(t), 0)
	var np *AccountNotPendingError
	if !errors.As(err, &np) || !np.HasCredential {
		t.Fatalf("err = %v, want *AccountNotPendingError with HasCredential", err)
	}
	if stored, _ := s.GetUser(ctx, u.ID); stored.Lifecycle != LifecyclePending {
		t.Fatalf("lifecycle = %s, want pending", stored.Lifecycle)
	}
}

func TestActivateWithCertBinding_RefusesAKeyBoundToAnotherAccount(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	admin, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "admin", TenantID: "acme", WithoutEmailLoginKey: true})
	invitee, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	tp := freshThumbprint(t)
	if _, err := s.AddCertBinding(ctx, admin.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	_, err := s.ActivateWithCertBinding(ctx, invitee.ID, tp, 0)
	var conflict *CertBindingConflictError
	if !errors.As(err, &conflict) || conflict.BoundUserID != admin.ID {
		t.Fatalf("err = %v, want *CertBindingConflictError naming the admin", err)
	}
	if stored, _ := s.GetUser(ctx, invitee.ID); stored.Lifecycle != LifecyclePending {
		t.Fatalf("lifecycle = %s, want pending", stored.Lifecycle)
	}
}

func TestActivateWithCertBinding_ValidatesTheBinding(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	var ve *ValidationError
	if _, err := s.ActivateWithCertBinding(ctx, u.ID, "", 0); !errors.As(err, &ve) {
		t.Fatalf("empty thumbprint: %v", err)
	}
	if _, err := s.ActivateWithCertBinding(ctx, u.ID, "not-canonical", 0); !errors.As(err, &ve) {
		t.Fatalf("non-canonical thumbprint: %v", err)
	}
	var expired *CertBindingExpiredError
	if _, err := s.ActivateWithCertBinding(ctx, u.ID, freshThumbprint(t), time.Now().Add(-time.Minute).Unix()); !errors.As(err, &expired) {
		t.Fatalf("expired binding: %v", err)
	}
	var nf *UserNotFoundError
	if _, err := s.ActivateWithCertBinding(ctx, u.ID+1, freshThumbprint(t), 0); !errors.As(err, &nf) {
		t.Fatalf("missing account: %v", err)
	}
	if _, err := s.ActivateWithCertBinding(ctx, 0, freshThumbprint(t), 0); !errors.As(err, &ve) {
		t.Fatalf("zero id: %v", err)
	}
}

// plainKVStore wraps a *memKVStore but forwards only kvstore.KVStore's base
// methods, deliberately NOT PutIfAbsent/CompareAndDelete/CompareAndSwap, so
// it does not satisfy kvstore.AtomicKVStore -- embedding the pointer
// directly would promote those methods and defeat the point.
type plainKVStore struct {
	inner *memKVStore
}

func (p *plainKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	return p.inner.Get(ctx, key)
}
func (p *plainKVStore) Put(ctx context.Context, key string, value []byte) error {
	return p.inner.Put(ctx, key, value)
}
func (p *plainKVStore) Delete(ctx context.Context, key string) error { return p.inner.Delete(ctx, key) }
func (p *plainKVStore) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return p.inner.Scan(ctx, prefix, fn)
}
func (p *plainKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return p.inner.List(ctx, prefix)
}
func (p *plainKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return p.inner.Exists(ctx, key)
}
func (p *plainKVStore) RegisterEntityIndexes(name string, idx []kvstore.EntityIndex) error {
	return p.inner.RegisterEntityIndexes(name, idx)
}
func (p *plainKVStore) QueryIndex(ctx context.Context, name string, key []byte) ([][]byte, error) {
	return p.inner.QueryIndex(ctx, name, key)
}
func (p *plainKVStore) ScanIndex(ctx context.Context, name string, start, end []byte) (map[string][][]byte, error) {
	return p.inner.ScanIndex(ctx, name, start, end)
}
func (p *plainKVStore) QueryCompoundIndex(ctx context.Context, name string, keys [][]byte) ([][]byte, error) {
	return p.inner.QueryCompoundIndex(ctx, name, keys)
}
func (p *plainKVStore) ScanCompoundIndex(ctx context.Context, name string, keys [][]byte, start, end []byte) (map[string][][]byte, error) {
	return p.inner.ScanCompoundIndex(ctx, name, keys, start, end)
}

// TestActivateWithCertBinding_RequiresAnAtomicStore: a store that cannot
// guarantee the conditional commit is refused before any read, the same
// check mutateCertBindings applies to AddCertBinding.
func TestActivateWithCertBinding_RequiresAnAtomicStore(t *testing.T) {
	ctx := context.Background()
	backing := newMemKVStore()
	// Seeded through an ATOMIC view: creating the account needs PutIfAbsent
	// to claim its username index entry. Only the activation call itself
	// goes through the plain, non-atomic view over the SAME backing data.
	seeded := &kvUserStore{store: backing, idgen: newTestIDGen(t)}
	u, err := seeded.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	store := &kvUserStore{store: &plainKVStore{inner: backing}, idgen: newTestIDGen(t)}
	if _, err := store.ActivateWithCertBinding(ctx, u.ID, freshThumbprint(t), 0); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("err = %v, want ErrStoreNotAtomic", err)
	}
}

// TestActivateWithCertBinding_StorageFailures exercises the primary and
// index lookup failure paths: a store whose Get call fails reports a
// *StoreError rather than panicking or silently activating the account.
func TestActivateWithCertBinding_StorageFailures(t *testing.T) {
	seed := newMemKVStore()
	seeded, err := NewStore(seed, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	u, err := seeded.(*kvUserStore).CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	boom := errors.New("storage down")

	// The 1st Get is the primary lookup.
	primaryFail := &faultKVStore{inner: seed, getErr: boom, getFailAt: 1}
	primaryStore := &kvUserStore{store: primaryFail, idgen: newTestIDGen(t)}
	_, err = primaryStore.ActivateWithCertBinding(ctx, u.ID, freshThumbprint(t), 0)
	var se *StoreError
	if !errors.As(err, &se) || se.Detail != "primary lookup" {
		t.Fatalf("primary lookup failure: err = %v", err)
	}

	// The 2nd Get is the thumbprint index lookup.
	indexFail := &faultKVStore{inner: seed, getErr: boom, getFailAt: 2}
	indexStore := &kvUserStore{store: indexFail, idgen: newTestIDGen(t)}
	_, err = indexStore.ActivateWithCertBinding(ctx, u.ID, freshThumbprint(t), 0)
	if !errors.As(err, &se) || se.Detail != "index lookup" {
		t.Fatalf("index lookup failure: err = %v", err)
	}
}

// Two activations of one pending account race: exactly one commits, and the
// account ends with exactly that one's key.
func TestActivateWithCertBinding_ConcurrentActivationsHaveOneWinner(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})

	const racers = 8
	keys := make([]string, racers)
	for i := range keys {
		keys[i] = freshThumbprint(t)
	}
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.ActivateWithCertBinding(ctx, u.ID, keys[i], 0)
		}()
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			continue
		}
		var np *AccountNotPendingError
		var lost *ConcurrentUpdateError
		if !errors.As(err, &np) && !errors.As(err, &lost) {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d activations committed, want exactly 1", winners)
	}
	stored, _ := s.GetUser(ctx, u.ID)
	if len(stored.CertBindings) != 1 || stored.Lifecycle != LifecycleActive {
		t.Fatalf("stored = %s with %d bindings, want active with 1", stored.Lifecycle, len(stored.CertBindings))
	}
}

func TestRoutingUserService_ActivateWithCertBindingRoutesToTheOwningTenant(t *testing.T) {
	tenantStore := newFakeKVStore()
	resolver := &fakeResolver{shared: newFakeKVStore(), tenants: map[string]kvstore.KVStore{"secure": tenantStore}}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "secure")
	u, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "invitee", TenantID: "secure", Pending: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	activator, ok := svc.(CertActivator)
	if !ok {
		t.Fatal("the routing service does not implement CertActivator")
	}
	tp := freshThumbprint(t)
	got, err := activator.ActivateWithCertBinding(ctx, u.ID, tp, 0)
	if err != nil || got.Lifecycle != LifecycleActive {
		t.Fatalf("ActivateWithCertBinding = %v, %v", got, err)
	}
	// Another tenant's context does not reach the record.
	other := tenant.WithTenantID(context.Background(), "elsewhere")
	var nf *UserNotFoundError
	if _, err := activator.ActivateWithCertBinding(other, u.ID, freshThumbprint(t), 0); !errors.As(err, &nf) {
		t.Fatalf("cross-tenant activation: err = %v, want *UserNotFoundError", err)
	}
}

func TestAccountNotPendingError_Message(t *testing.T) {
	if got := (&AccountNotPendingError{ID: 7, Lifecycle: LifecycleActive}).Error(); got != "user: account 7 is active, not pending" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&AccountNotPendingError{ID: 7, Lifecycle: LifecyclePending, HasCredential: true}).Error(); got != "user: account 7 already holds a certificate binding" {
		t.Fatalf("Error() = %q", got)
	}
}
