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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// memPendingAdminCertStore is an in-memory PendingAdminCertStore for testing.
type memPendingAdminCertStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemPendingAdminCertStore() *memPendingAdminCertStore {
	return &memPendingAdminCertStore{data: map[string][]byte{}}
}

func (s *memPendingAdminCertStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[key]; exists {
		return false, nil
	}
	s.data[key] = value
	return true, nil
}

func (s *memPendingAdminCertStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}

func (s *memPendingAdminCertStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// noAdminLookup always reports the admin missing, for tests that only care
// about the pending-record half of AwaitPendingAdminCert.
type noAdminLookup struct{}

func (noAdminLookup) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return nil, &user.UserNotFoundError{}
}

// TestWritePendingAdminCert_NilStoreIsNoOp covers the no-op guards.
func TestWritePendingAdminCert_NilStoreIsNoOp(t *testing.T) {
	if err := WritePendingAdminCert(context.Background(), nil, []byte("cert")); err != nil {
		t.Fatalf("nil store: %v", err)
	}
	store := newMemPendingAdminCertStore()
	if err := WritePendingAdminCert(context.Background(), store, nil); err != nil {
		t.Fatalf("empty cert: %v", err)
	}
	if _, err := store.Get(context.Background(), PendingAdminCertKey); err == nil {
		t.Fatal("an empty certPEM must not write a record")
	}
}

// TestWriteThenAwaitPendingAdminCert_RoundTrips covers the common path: a
// written record is read back by AwaitPendingAdminCert.
func TestWriteThenAwaitPendingAdminCert_RoundTrips(t *testing.T) {
	store := newMemPendingAdminCertStore()
	if err := WritePendingAdminCert(context.Background(), store, []byte("cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert: %v", err)
	}
	got, err := AwaitPendingAdminCert(context.Background(), store, noAdminLookup{}, time.Second)
	if err != nil {
		t.Fatalf("AwaitPendingAdminCert: %v", err)
	}
	if string(got) != "cert-pem" {
		t.Fatalf("got %q, want cert-pem", got)
	}
}

// TestAwaitPendingAdminCert_AlreadyBoundSkips covers the already-satisfied
// case: nothing pending is needed when the admin already has a live binding.
func TestAwaitPendingAdminCert_AlreadyBoundSkips(t *testing.T) {
	bound := fakeAdminLookupBound{}
	got, err := AwaitPendingAdminCert(context.Background(), nil, bound, time.Second)
	if err != nil || got != nil {
		t.Fatalf("AwaitPendingAdminCert = (%v, %v), want (nil, nil)", got, err)
	}
}

type fakeAdminLookupBound struct{}

func (fakeAdminLookupBound) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	return &user.User{
		Username: adminUsername, TenantID: tenantid.System, Lifecycle: user.LifecycleActive,
		CertBindings: []user.CertBinding{{Thumbprint: "thumb", Kind: user.CertBindingSPKIThumbprint}},
	}, nil
}

// TestAwaitPendingAdminCert_TimesOutWithTypedError covers the failure path:
// nothing pending and no binding within the bound.
func TestAwaitPendingAdminCert_TimesOutWithTypedError(t *testing.T) {
	_, err := AwaitPendingAdminCert(context.Background(), newMemPendingAdminCertStore(), noAdminLookup{}, 10*time.Millisecond)
	var noCred *NoPendingAdminCredentialError
	if !errors.As(err, &noCred) {
		t.Fatalf("err = %T(%v), want *NoPendingAdminCredentialError", err, err)
	}
}

// TestClearPendingAdminCert_Idempotent covers delete-then-delete-again and the
// nil-store no-op.
func TestClearPendingAdminCert_Idempotent(t *testing.T) {
	if err := ClearPendingAdminCert(context.Background(), nil); err != nil {
		t.Fatalf("nil store: %v", err)
	}
	store := newMemPendingAdminCertStore()
	_ = WritePendingAdminCert(context.Background(), store, []byte("x"))
	if err := ClearPendingAdminCert(context.Background(), store); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	if err := ClearPendingAdminCert(context.Background(), store); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

// failingPendingAdminCertStore always refuses Put/Get/Delete with cause, so
// WritePendingAdminCert / ClearPendingAdminCert's wrapping can be proven.
type failingPendingAdminCertStore struct{ cause error }

func (s failingPendingAdminCertStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, s.cause
}
func (s failingPendingAdminCertStore) Get(context.Context, string) ([]byte, error) {
	return nil, s.cause
}
func (s failingPendingAdminCertStore) Delete(context.Context, string) error { return s.cause }

// TestWritePendingAdminCert_StoreErrorWraps covers the Put-failure path and
// PendingAdminCertStoreError's Error/Unwrap.
func TestWritePendingAdminCert_StoreErrorWraps(t *testing.T) {
	cause := errors.New("write refused")
	err := WritePendingAdminCert(context.Background(), failingPendingAdminCertStore{cause: cause}, []byte("cert"))
	var storeErr *PendingAdminCertStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("err = %T(%v), want *PendingAdminCertStoreError", err, err)
	}
	if storeErr.Op != "write" {
		t.Fatalf("Op = %q, want %q", storeErr.Op, "write")
	}
	if !errors.Is(storeErr, cause) {
		t.Fatalf("Unwrap did not expose cause: %v", storeErr.Unwrap())
	}
	if got, want := storeErr.Error(), "firstboot: pending admin credential store write failed: write refused"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestClearPendingAdminCert_StoreErrorWraps covers the Delete-failure path.
func TestClearPendingAdminCert_StoreErrorWraps(t *testing.T) {
	cause := errors.New("delete refused")
	err := ClearPendingAdminCert(context.Background(), failingPendingAdminCertStore{cause: cause})
	var storeErr *PendingAdminCertStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("err = %T(%v), want *PendingAdminCertStoreError", err, err)
	}
	if storeErr.Op != "clear" {
		t.Fatalf("Op = %q, want %q", storeErr.Op, "clear")
	}
}

// TestAwaitPendingAdminCert_AlreadyPastDeadlineRefusesImmediately covers the
// top-of-loop deadline check firing on the very first iteration, without ever
// reaching the backoff sleep.
func TestAwaitPendingAdminCert_AlreadyPastDeadlineRefusesImmediately(t *testing.T) {
	_, err := AwaitPendingAdminCert(context.Background(), newMemPendingAdminCertStore(), noAdminLookup{}, 0)
	var noCred *NoPendingAdminCredentialError
	if !errors.As(err, &noCred) {
		t.Fatalf("err = %T(%v), want *NoPendingAdminCredentialError", err, err)
	}
	if got, want := noCred.Error(), `firstboot: no admin certificate binding and no pending admin credential record; re-run "qrdb init" (with --admin-csr, --admin-cert, or no flags to generate) to retry the admin-credential step`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestWritePendingAdminCert_ConcurrentNodesRaceDeterministically covers the
// multi-node bootstrap race (go-qrdb#548): N nodes each ran the sealed-window
// ceremony and each got back a DIFFERENT CA-issued credential, then all race
// to call WritePendingAdminCert concurrently. Exactly one write must land
// (stored), and every other caller must get
// *AdminCredentialAlreadyPendingError deterministically, never a second
// silent overwrite of the record first boot will bind.
func TestWritePendingAdminCert_ConcurrentNodesRaceDeterministically(t *testing.T) {
	const nodes = 8
	store := newMemPendingAdminCertStore()

	var wg sync.WaitGroup
	errs := make([]error, nodes)
	for i := 0; i < nodes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			certPEM := []byte("cert-from-node-" + string(rune('A'+i)))
			errs[i] = WritePendingAdminCert(context.Background(), store, certPEM)
		}(i)
	}
	wg.Wait()

	var wins, pending int
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.As(err, new(*AdminCredentialAlreadyPendingError)):
			pending++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1", wins)
	}
	if pending != nodes-1 {
		t.Fatalf("pending = %d, want %d", pending, nodes-1)
	}

	// The record that landed is one of the nodes' own credentials, not a
	// corrupted interleaving of two writes.
	got, err := store.Get(context.Background(), PendingAdminCertKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no pending record landed despite one winner")
	}
}

// TestAwaitPendingAdminCert_BackoffClampsAtOneSecond covers the doubling
// backoff's clamp, which only fires once the interval would otherwise exceed
// one second.
func TestAwaitPendingAdminCert_BackoffClampsAtOneSecond(t *testing.T) {
	start := time.Now()
	_, err := AwaitPendingAdminCert(context.Background(), newMemPendingAdminCertStore(), noAdminLookup{}, 1300*time.Millisecond)
	var noCred *NoPendingAdminCredentialError
	if !errors.As(err, &noCred) {
		t.Fatalf("err = %T(%v), want *NoPendingAdminCredentialError", err, err)
	}
	if elapsed := time.Since(start); elapsed < 1300*time.Millisecond {
		t.Fatalf("returned after %v, want >= 1.3s", elapsed)
	}
}
