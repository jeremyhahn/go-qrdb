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

package vector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

const vectorQuiescePassphrase = "vector-quiesce-passphrase"

// newVectorQuiesceBarrier is an initialized, unsealed StorageBarrier under
// cfg.
func newVectorQuiesceBarrier(t *testing.T, cfg seal.BarrierConfig) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(vectorQuiescePassphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	cfg.RootKeyPath = "vector-quiesce-root-key"
	b, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(), cfg, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, seal.MustCredentials(ctx, []byte(vectorQuiescePassphrase))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

// holdLeasedWrite arms leasedWriteHook so the next write blocks inside its
// quiesce lease: entered closes once it is held, and closing release lets
// it continue.
func holdLeasedWrite(t *testing.T) (entered, release chan struct{}) {
	t.Helper()
	entered = make(chan struct{})
	release = make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	leasedWriteHook = func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	}
	t.Cleanup(func() { leasedWriteHook = nil })
	return entered, release
}

// openSealedVectorStore opens a vector store encrypted under b, with one
// namespace.
func openSealedVectorStore(t *testing.T, b *seal.StorageBarrier) (*PebbleVectorStorage, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewPebbleVectorStorage(DefaultConfig(dir), &testLogger{}, WithEncryptor(atrest.New(b)))
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateNamespace(context.Background(), &NamespaceInfo{Name: "ns", Dimension: 2}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	return s, dir
}

func upsert(s *PebbleVectorStorage, id uint64) error {
	return s.UpsertVector(context.Background(), "ns", &VectorValue{ID: id, Embedding: []float32{0.1, 0.2}, Version: 1})
}

// TestVectorStorage_SealDrainsAWriteInFlight proves a vector write held
// inside its quiesce lease keeps Seal from reporting the store drained, that
// Seal completes once it returns, and that a write after the seal is refused
// with the typed sealed error.
func TestVectorStorage_SealDrainsAWriteInFlight(t *testing.T) {
	b := newVectorQuiesceBarrier(t, seal.BarrierConfig{SealDrainTimeout: time.Minute})
	s, _ := openSealedVectorStore(t, b)

	entered, release := holdLeasedWrite(t)
	written := make(chan error, 1)
	go func() { written <- upsert(s, 1) }()
	<-entered
	sealed := make(chan error, 1)
	go func() { sealed <- b.Seal() }()
	for !b.Quiesce().Closed() {
		runtime.Gosched()
	}
	select {
	case err := <-sealed:
		t.Fatalf("Seal returned %v with a vector write in flight", err)
	default:
	}
	if b.IsSealed() {
		t.Fatal("the barrier sealed with a vector write in flight")
	}

	close(release)
	if err := <-written; err != nil {
		t.Fatalf("the write in flight = %v", err)
	}
	if err := <-sealed; err != nil {
		t.Fatalf("Seal = %v", err)
	}
	var refused *pebblestore.SealedError
	if err := upsert(s, 2); !errors.As(err, &refused) {
		t.Fatalf("UpsertVector while sealed = %v; want a *SealedError", err)
	}
}

// TestVectorStorage_SealTimesOutNamingTheStore holds a vector write past the
// drain bound: Seal still seals and names the vector store's directory.
func TestVectorStorage_SealTimesOutNamingTheStore(t *testing.T) {
	b := newVectorQuiesceBarrier(t, seal.BarrierConfig{SealDrainTimeout: time.Millisecond})
	s, dir := openSealedVectorStore(t, b)

	entered, release := holdLeasedWrite(t)
	written := make(chan error, 1)
	go func() { written <- upsert(s, 1) }()
	<-entered
	err := b.Seal()
	var timeout *quiesce.DrainTimeoutError
	if !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{dir}) {
		t.Fatalf("Seal = %v; want a DrainTimeoutError naming %s", err, dir)
	}
	if !b.IsSealed() {
		t.Fatal("a drain timeout vetoed the seal")
	}
	close(release)
	<-written

	var refused *pebblestore.SealedError
	if _, err := s.NextID("ns"); err != nil {
		t.Fatalf("NextID = %v; an ID is allocated in memory while sealed", err)
	}
	if err := upsert(s, 2); !errors.As(err, &refused) {
		t.Fatalf("UpsertVector while sealed = %v; want a *SealedError", err)
	}
	if err := s.Compact(context.Background(), "ns"); !errors.As(err, &refused) {
		t.Fatalf("Compact while sealed = %v; want a *SealedError", err)
	}
}
