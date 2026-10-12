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
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

func (s *memPendingAdminCertStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
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

// TestWriteIssuedAdminCertIfAny_NilCoreIsNoOp covers the no-op guard: a nil
// ceremony core (go-qrdb#548 review item 6) is a no-op, not an error.
func TestWriteIssuedAdminCertIfAny_NilCoreIsNoOp(t *testing.T) {
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedAdminCertIfAny(context.Background(), nil, store, nil))
	_, err := store.Get(context.Background(), PendingAdminCertKey)
	require.Error(t, err, "a nil core must not write a record")
}

// TestWriteIssuedAdminCertIfAny_NothingIssuedIsNoOp covers a real ceremony
// core that has not issued anything yet: still a no-op.
func TestWriteIssuedAdminCertIfAny_NothingIssuedIsNoOp(t *testing.T) {
	core := testCeremonyCore(t)
	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedAdminCertIfAny(context.Background(), core, store, nil))
	_, err := store.Get(context.Background(), PendingAdminCertKey)
	require.Error(t, err, "a core with nothing issued must not write a record")
}

// TestWriteIssuedAdminCertIfAny_WritesWhatWasIssued covers the common path:
// a ceremony core that actually issued a bootstrap admin certificate (via
// Initialize) has it recorded into the pending-admin-cert store.
func TestWriteIssuedAdminCertIfAny_WritesWhatWasIssued(t *testing.T) {
	core := testCeremonyCore(t)
	ca, _ := ceremonytest.NewCA(t, "test-ca")
	core.SetPINBackend(pintest.NewBackend(t))
	_, err := core.Initialize(context.Background(), ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	wantCertPEM, _ := core.LastIssuedAdminCertPEM()
	require.NotEmpty(t, wantCertPEM)

	store := newMemPendingAdminCertStore()
	require.NoError(t, WriteIssuedAdminCertIfAny(context.Background(), core, store, nil))
	got, err := store.Get(context.Background(), PendingAdminCertKey)
	require.NoError(t, err)
	require.Equal(t, wantCertPEM, got)
}

// TestWriteIssuedAdminCertIfAny_TheRaceLoserIsNotAnError covers the
// create-if-absent race: a record already written by another node is
// tolerated, not surfaced as an error -- this node's own credential simply
// was not the one that won.
func TestWriteIssuedAdminCertIfAny_TheRaceLoserIsNotAnError(t *testing.T) {
	core := testCeremonyCore(t)
	ca, _ := ceremonytest.NewCA(t, "test-ca")
	core.SetPINBackend(pintest.NewBackend(t))
	_, err := core.Initialize(context.Background(), ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)

	store := newMemPendingAdminCertStore()
	require.NoError(t, WritePendingAdminCert(context.Background(), store, []byte("another-node-won")))

	require.NoError(t, WriteIssuedAdminCertIfAny(context.Background(), core, store, nil))
	got, err := store.Get(context.Background(), PendingAdminCertKey)
	require.NoError(t, err)
	require.Equal(t, []byte("another-node-won"), got, "the winning node's record must be left untouched")
}

// testCeremonyCore builds a minimal, real *ceremony.CeremonyService over an
// in-memory barrier -- the concrete type WriteIssuedAdminCertIfAny reads
// LastIssuedAdminCertPEM from, so a test double cannot stand in for it.
func testCeremonyCore(t *testing.T) *ceremony.CeremonyService {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-password"))
	require.NoError(t, err)
	barrier, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	require.NoError(t, err)
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return core
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

// TestClearPendingAdminCert_Idempotent covers consume-then-consume-again
// and the nil-store no-op.
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

// TestClearPendingAdminCert_NothingPendingIsANoOp is the regression test for
// go-qrdb#560 review B2: a prior version wrote the "consumed" marker
// UNCONDITIONALLY, every time first boot ran, including the many boots
// where nothing was ever pending at all (an auth-disabled node, for one --
// it never runs a ceremony, so the key never exists -- but also, on ANY
// node, every boot after the first). That poisoned the key permanently:
// WritePendingAdminCert's create-if-absent could never succeed again, so a
// LATER ceremony on this same cluster could never hand a credential to
// first boot at all. Clearing an empty store must leave the key absent,
// not write the marker over it.
func TestClearPendingAdminCert_NothingPendingIsANoOp(t *testing.T) {
	store := newMemPendingAdminCertStore()
	if err := ClearPendingAdminCert(context.Background(), store); err != nil {
		t.Fatalf("ClearPendingAdminCert on an empty store: %v", err)
	}
	if _, err := store.Get(context.Background(), PendingAdminCertKey); err == nil {
		t.Fatal("ClearPendingAdminCert must not create the record when nothing was pending")
	}

	// A ceremony running LATER on this same cluster must still be able to
	// record its credential: the key must not have been poisoned to
	// "consumed" while nothing was ever pending.
	if err := WritePendingAdminCert(context.Background(), store, []byte("later-cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert after a no-op clear: %v", err)
	}
}

// TestClearPendingAdminCert_NeverDeletesTheRecord is the regression test for
// go-qrdb#560 review item 8: the pending-admin-cert record is a one-way
// state machine, pending then consumed, and is NEVER deleted. The old
// behavior (a real Delete) reopened WritePendingAdminCert's create-if-absent
// race after the FIRST binding had already committed: a late-arriving node
// -- one that ran the sealed-window ceremony and got its own CA-issued
// credential, but reached this call only after ClearPendingAdminCert had
// already run for the winning node -- would see the key absent again and
// successfully create a SECOND pending record for a credential that will
// never be bound, which AwaitPendingAdminCert on some THIRD node could then
// read and hand to an operator as if it were authoritative.
func TestClearPendingAdminCert_NeverDeletesTheRecord(t *testing.T) {
	store := newMemPendingAdminCertStore()
	if err := WritePendingAdminCert(context.Background(), store, []byte("winning-cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert: %v", err)
	}
	if err := ClearPendingAdminCert(context.Background(), store); err != nil {
		t.Fatalf("ClearPendingAdminCert: %v", err)
	}

	// The record must still exist (consumed, not absent) --
	// PutIfAbsent-based re-creation must never succeed again.
	err := WritePendingAdminCert(context.Background(), store, []byte("late-arriving-cert-pem"))
	var alreadyPending *AdminCredentialAlreadyPendingError
	if !errors.As(err, &alreadyPending) {
		t.Fatalf("late WritePendingAdminCert after consume = %v, want *AdminCredentialAlreadyPendingError", err)
	}

	// A third node's AwaitPendingAdminCert must not read the consumed
	// marker back as if it were a real, actionable certificate: the
	// record's mere presence must not override the admin-already-bound
	// check this call makes first for an identity lookup that still
	// reports no binding (simulating replication lag).
	got, err := AwaitPendingAdminCert(context.Background(), store, noAdminLookup{}, 10*time.Millisecond)
	if err == nil {
		t.Fatalf("AwaitPendingAdminCert returned the consumed marker as a certificate: %q", got)
	}
	var noCred *NoPendingAdminCredentialError
	if !errors.As(err, &noCred) {
		t.Fatalf("err = %T(%v), want *NoPendingAdminCredentialError", err, err)
	}
}

// failingPendingAdminCertStore always refuses PutIfAbsent/Get/Put/Delete
// with cause, so WritePendingAdminCert / ClearPendingAdminCert's wrapping
// can be proven.
type failingPendingAdminCertStore struct{ cause error }

func (s failingPendingAdminCertStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, s.cause
}
func (s failingPendingAdminCertStore) Get(context.Context, string) ([]byte, error) {
	return nil, s.cause
}
func (s failingPendingAdminCertStore) Put(context.Context, string, []byte) error { return s.cause }
func (s failingPendingAdminCertStore) Delete(context.Context, string) error      { return s.cause }

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

// putFailsStore wraps a real PendingAdminCertStore, answering Get from it
// unchanged but always refusing Put with cause -- so a test can exercise a
// Put failure on a record that genuinely IS pending (Get succeeds),
// distinct from a Get failure (which ClearPendingAdminCert now treats as
// "nothing to clear", go-qrdb#560 review B2).
type putFailsStore struct {
	PendingAdminCertStore
	cause error
}

func (s putFailsStore) Put(context.Context, string, []byte) error { return s.cause }

// TestClearPendingAdminCert_StoreErrorWraps covers the Put-failure path
// (the consume-in-place write, not a delete) on a record that IS pending --
// Get must succeed and report a real certificate for Put to even be
// attempted; see TestClearPendingAdminCert_NothingPendingIsANoOp for the
// Get-failure/nothing-pending case, which is a no-op, not an error.
func TestClearPendingAdminCert_StoreErrorWraps(t *testing.T) {
	store := newMemPendingAdminCertStore()
	if err := WritePendingAdminCert(context.Background(), store, []byte("cert-pem")); err != nil {
		t.Fatalf("WritePendingAdminCert: %v", err)
	}
	cause := errors.New("consume refused")
	err := ClearPendingAdminCert(context.Background(), putFailsStore{PendingAdminCertStore: store, cause: cause})
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
	if got, want := noCred.Error(), `firstboot: no admin certificate binding and no pending admin credential record; re-run "qrdb init" (with exactly one of --admin-csr, --admin-cert or --admin-generate) to retry the admin-credential step`; got != want {
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
