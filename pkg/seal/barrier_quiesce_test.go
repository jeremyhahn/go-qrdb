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
	"io"
	"log/slog"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

const quiescePass = "quiesce-test-passphrase"

// newQuiesceBarrier is an initialized, unsealed software barrier under cfg.
func newQuiesceBarrier(t *testing.T, cfg BarrierConfig) *StorageBarrier {
	t.Helper()
	strategy, err := NewSoftwareStrategy(context.Background(), []byte(quiescePass))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	cfg.RootKeyPath = "quiesce/root-key"
	b, err := NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), NewMemoryBackend(), cfg, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), MustCredentials(context.Background(), []byte(quiescePass))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

// awaitClosed returns once Seal has closed the gate, without sleeping.
func awaitClosed(g *quiesce.Gate) {
	for !g.Closed() {
		runtime.Gosched()
	}
}

func TestBarrierConfig_SealDrainTimeoutDefaults(t *testing.T) {
	if got := (BarrierConfig{}).sealDrainTimeout(); got != DefaultSealDrainTimeout {
		t.Fatalf("zero SealDrainTimeout resolved to %s; want %s", got, DefaultSealDrainTimeout)
	}
}

func TestBarrierConfig_SealDrainTimeoutKeepsAnExplicitValue(t *testing.T) {
	for _, d := range []time.Duration{time.Millisecond, -1} {
		if got := (BarrierConfig{SealDrainTimeout: d}).sealDrainTimeout(); got != d {
			t.Fatalf("SealDrainTimeout %s resolved to %s", d, got)
		}
	}
}

func TestStorageBarrier_QuiesceIsOneGate(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{})
	if b.Quiesce() == nil || b.Quiesce() != b.Quiesce() {
		t.Fatal("Quiesce() is not one stable gate")
	}
}

func TestStorageBarrier_QuiesceIsOpenWhileUnsealed(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{})
	if b.Quiesce().Closed() {
		t.Fatal("the gate of an unsealed barrier is closed")
	}
}

func TestStorageBarrier_SealWaitsForAWriteInFlight(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{SealDrainTimeout: time.Minute})
	lease := b.Quiesce().Register("db")
	if err := lease.Acquire(); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan error, 1)
	go func() { sealed <- b.Seal() }()
	awaitClosed(b.Quiesce())
	select {
	case err := <-sealed:
		t.Fatalf("Seal returned %v with a write in flight", err)
	default:
	}
	if b.IsSealed() {
		t.Fatal("the barrier sealed with a write in flight")
	}
	lease.Release()
	if err := <-sealed; err != nil {
		t.Fatalf("Seal = %v", err)
	}
	if !b.IsSealed() {
		t.Fatal("IsSealed() = false after Seal")
	}
}

func TestStorageBarrier_SealRefusesNewWritesWhileSealing(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{SealDrainTimeout: time.Minute})
	held, other := b.Quiesce().Register("held"), b.Quiesce().Register("other")
	if err := held.Acquire(); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan error, 1)
	go func() { sealed <- b.Seal() }()
	awaitClosed(b.Quiesce())
	if err := other.Acquire(); !errors.Is(err, quiesce.ErrSealing) {
		t.Fatalf("Acquire while sealing = %v; want ErrSealing", err)
	}
	held.Release()
	if err := <-sealed; err != nil {
		t.Fatalf("Seal = %v", err)
	}
}

func TestStorageBarrier_SealTimesOutButStillSeals(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{SealDrainTimeout: time.Millisecond})
	lease := b.Quiesce().Register("stuck-store")
	if err := lease.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	err := b.Seal()
	var timeout *quiesce.DrainTimeoutError
	if !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{"stuck-store"}) {
		t.Fatalf("Seal = %v; want a DrainTimeoutError naming stuck-store", err)
	}
	if !b.IsSealed() {
		t.Fatal("a drain timeout vetoed the seal")
	}
}

func TestStorageBarrier_SealWithNoWaitReportsAtOnce(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{SealDrainTimeout: -1})
	lease := b.Quiesce().Register("db")
	if err := lease.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := b.Seal(); !errors.Is(err, quiesce.ErrDrainTimeout) {
		t.Fatalf("Seal = %v; want ErrDrainTimeout", err)
	}
	if !b.IsSealed() {
		t.Fatal("IsSealed() = false")
	}
}

func TestStorageBarrier_SealJoinsObserverAndDrainErrors(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{SealDrainTimeout: -1})
	observerErr := errors.New("observer failed")
	b.OnSealing(func(context.Context) error { return observerErr })
	lease := b.Quiesce().Register("db")
	if err := lease.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	err := b.Seal()
	if !errors.Is(err, observerErr) || !errors.Is(err, quiesce.ErrDrainTimeout) {
		t.Fatalf("Seal = %v; want both the observer and the drain error", err)
	}
}

func TestStorageBarrier_SealingObserversRunBeforeTheGateCloses(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{})
	lease := b.Quiesce().Register("volume")
	var observed error
	b.OnSealing(func(context.Context) error {
		// A store an observer closes still flushes through the gate.
		observed = lease.Acquire()
		if observed == nil {
			lease.Release()
		}
		return nil
	})
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal = %v", err)
	}
	if observed != nil {
		t.Fatalf("an observer's flush was refused: %v", observed)
	}
}

func TestStorageBarrier_UnsealReopensTheGate(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{})
	lease := b.Quiesce().Register("db")
	if err := b.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Acquire(); !errors.Is(err, quiesce.ErrSealing) {
		t.Fatalf("Acquire while sealed = %v; want ErrSealing", err)
	}
	if err := b.Unseal(context.Background(), MustCredentials(context.Background(), []byte(quiescePass))); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := lease.Acquire(); err != nil {
		t.Fatalf("Acquire after unseal = %v", err)
	}
	lease.Release()
}

func TestStorageBarrier_MarkUnsealedOpensTheGateAfterTheState(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{})
	if err := b.Seal(); err != nil {
		t.Fatal(err)
	}
	b.markUnsealed()
	if b.IsSealed() || b.Quiesce().Closed() {
		t.Fatalf("after markUnsealed: sealed=%v closed=%v", b.IsSealed(), b.Quiesce().Closed())
	}
	b.markUnsealed()
	if b.IsSealed() || b.Quiesce().Closed() {
		t.Fatal("markUnsealed is not idempotent")
	}
}

func TestStorageBarrier_QuiesceStoresDrainsAnIdleGate(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{})
	b.Quiesce().Register("idle")
	if err := b.quiesceStores(); err != nil {
		t.Fatalf("quiesceStores = %v", err)
	}
	if !b.Quiesce().Closed() {
		t.Fatal("quiesceStores left the gate open")
	}
}

func TestStorageBarrier_QuiesceStoresReportsAHeldStore(t *testing.T) {
	b := newQuiesceBarrier(t, BarrierConfig{SealDrainTimeout: -1})
	lease := b.Quiesce().Register("held")
	if err := lease.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	var timeout *quiesce.DrainTimeoutError
	if err := b.quiesceStores(); !errors.As(err, &timeout) || timeout.Stores[0] != "held" {
		t.Fatalf("quiesceStores = %v; want a DrainTimeoutError naming held", err)
	}
}
