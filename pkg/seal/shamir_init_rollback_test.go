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

package seal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInitializeShamir_ExistsError(t *testing.T) {
	existsErr := errors.New("exists failed")
	shamirStrat, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStorageBarrier(testLogger(), &errorBackend{existsErr: existsErr}, BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
	}, shamirStrat)
	if err != nil {
		t.Fatal(err)
	}
	b.SetCeremonyDistributor(newCaptureDistributor("h1", "h2", "h3"))

	// The cause reaches the caller as text: InitializeShamir reports it under
	// ErrCorruptRootKey without wrapping it.
	if _, err := b.InitializeShamir(context.Background(), ""); err == nil || !strings.Contains(err.Error(), existsErr.Error()) {
		t.Fatalf("InitializeShamir: got %v, want an error carrying the backend's Exists error", err)
	}
}

// newUninitializedShamirBarrier returns a Shamir-configured barrier over a
// fresh memory backend with a capture distributor holding total holders.
func newUninitializedShamirBarrier(t *testing.T, threshold, total int) (*StorageBarrier, StorageBackend, *captureDistributor) {
	t.Helper()
	shamirStrategy, err := NewShamirStrategy(threshold, total)
	if err != nil {
		t.Fatal(err)
	}
	backend := NewMemoryBackend()
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &ShamirConfig{Threshold: threshold, TotalShares: total},
	}, shamirStrategy)
	if err != nil {
		t.Fatal(err)
	}
	dist := newCaptureDistributor(holderNames(total)...)
	b.SetCeremonyDistributor(dist)
	b.SetHolderVerifier(newTestVerifier())
	return b, backend, dist
}

// assertUninitialized fails the test unless b and its storage are exactly as
// an uninitialized barrier leaves them: sealed, no strategy, no
// initialization time, and no root key, wrapped key or metadata persisted.
func assertUninitialized(t *testing.T, b *StorageBarrier, backend StorageBackend) {
	t.Helper()
	ctx := context.Background()
	if !b.IsSealed() {
		t.Error("the barrier is unsealed")
	}
	for _, path := range []string{b.config.RootKeyPath, b.wrappedKeyPath(DomainUnseal), b.metadataPath()} {
		exists, err := backend.Exists(ctx, path)
		if err != nil {
			t.Fatalf("exists %s: %v", path, err)
		}
		if exists {
			t.Errorf("%s is still persisted", path)
		}
	}
	// Read before Status, which would itself learn the answer from the store.
	if got, known := b.InitializedCached(); got || !known {
		t.Errorf("InitializedCached after the rollback = (%v, %v), want (false, true)", got, known)
	}
	status := b.Status()
	if status.Strategy != "" {
		t.Errorf("status strategy = %q, want none", status.Strategy)
	}
	if !status.InitializedAt.IsZero() {
		t.Errorf("status initialized at %v, want never", status.InitializedAt)
	}
}

// TestInitializeShamir_DistributionFailureRollsBack proves a failed share
// distribution leaves no key behind: without the rollback the barrier stays
// initialized and unsealed with a root key no holder has a share of, and
// every retry — even after a restart — is refused as already initialized.
func TestInitializeShamir_DistributionFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	b, backend, dist := newUninitializedShamirBarrier(t, 2, 3)
	boom := errors.New("distribution failed")
	dist.mu.Lock()
	dist.failNext = boom
	dist.mu.Unlock()

	if _, err := b.InitializeShamir(ctx, ""); !errors.Is(err, boom) {
		t.Fatalf("InitializeShamir with a failing distributor = %v, want %v", err, boom)
	}
	assertUninitialized(t, b, backend)

	// The key material of the failed attempt cannot be un-generated in this
	// process, so an in-process retry is refused with a typed error rather
	// than reaching the epoch barrier.
	if _, err := b.InitializeShamir(ctx, ""); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("in-process retry = %v, want ErrRestartRequired", err)
	}
	assertUninitialized(t, b, backend)

	// After a restart — a new barrier over the same storage — initialization
	// is a fresh one, and its shares open the barrier.
	shamirStrategy, err := NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
	}, shamirStrategy)
	if err != nil {
		t.Fatal(err)
	}
	verifier := newTestVerifier()
	restarted.SetCeremonyDistributor(dist)
	restarted.SetHolderVerifier(verifier)
	params, err := restarted.InitializeShamir(ctx, "")
	if err != nil {
		t.Fatalf("InitializeShamir after the restart: %v", err)
	}
	if params.ShareCount != 3 {
		t.Fatalf("distributed %d shares, want 3", params.ShareCount)
	}
	f := &holderFixture{t: t, b: restarted, dist: dist, verifier: verifier}
	f.sealWith("h1", "h2")
	if o := f.quorum(DomainUnseal, OpUnseal, "h1", "h3"); !o.Complete {
		t.Fatal("the fresh initialization's shares do not unseal the barrier")
	}
}

// TestInitializeShamir_CommitFailureRollsBack proves the rollback covers the
// whole distribution, including the steps after the holders receive their
// shares: a failure recording the roster also leaves the barrier
// uninitialized.
func TestInitializeShamir_CommitFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	b, backend, _ := newUninitializedShamirBarrier(t, 2, 3)
	boom := errors.New("commit failed")
	b.SetCeremonyDistributor(&failingCommitDistributor{captureDistributor: newCaptureDistributor(holderNames(3)...), err: boom})

	if _, err := b.InitializeShamir(ctx, ""); !errors.Is(err, boom) {
		t.Fatalf("InitializeShamir with a failing commit = %v, want %v", err, boom)
	}
	assertUninitialized(t, b, backend)
}

// failingCommitDistributor distributes normally and fails Committed.
type failingCommitDistributor struct {
	*captureDistributor
	err error
}

// Committed implements CeremonyDistributor.
func (d *failingCommitDistributor) Committed(context.Context, *HolderRoster, []string) error {
	return d.err
}

var _ CeremonyDistributor = (*failingCommitDistributor)(nil)

// deleteFailingBackend is a working backend whose Delete fails for one path.
type deleteFailingBackend struct {
	StorageBackend
	path string
	err  error
}

// Delete implements StorageBackend.
func (d *deleteFailingBackend) Delete(ctx context.Context, key string) error {
	if key == d.path {
		return d.err
	}
	return d.StorageBackend.Delete(ctx, key)
}

// TestInitializeShamir_FailedRollbackReportsBothFailures proves a rollback
// that cannot delete what the failed initialization persisted says so: the
// error carries the distribution failure and the rollback failure, the
// barrier stays sealed, and an in-process retry is still refused.
func TestInitializeShamir_FailedRollbackReportsBothFailures(t *testing.T) {
	for _, path := range []string{"root-key", "root-key.meta"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			stuck := errors.New("delete failed")
			backend := &deleteFailingBackend{StorageBackend: NewMemoryBackend(), path: path, err: stuck}
			shamirStrategy, err := NewShamirStrategy(2, 3)
			if err != nil {
				t.Fatal(err)
			}
			b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{
				RootKeyPath: "root-key",
				Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
			}, shamirStrategy)
			if err != nil {
				t.Fatal(err)
			}
			dist := newCaptureDistributor(holderNames(3)...)
			boom := errors.New("distribution failed")
			dist.failNext = boom
			b.SetCeremonyDistributor(dist)
			b.SetHolderVerifier(newTestVerifier())

			_, err = b.InitializeShamir(ctx, "")
			var rbErr *ShamirInitRollbackError
			if !errors.As(err, &rbErr) {
				t.Fatalf("InitializeShamir = %v, want *ShamirInitRollbackError", err)
			}
			if !errors.Is(err, boom) || !errors.Is(err, stuck) || rbErr.Error() == "" {
				t.Fatalf("the error does not carry both failures: %v", err)
			}
			if !b.IsSealed() {
				t.Error("the barrier is unsealed after a failed rollback")
			}
			if _, err := b.InitializeShamir(ctx, ""); !errors.Is(err, ErrRestartRequired) && !errors.Is(err, ErrAlreadyInitialized) {
				t.Fatalf("retry after a failed rollback = %v", err)
			}
		})
	}
}
