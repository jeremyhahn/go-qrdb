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

package encfs_test

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

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

const quiescePassphrase = "encfs-quiesce-passphrase"

// newQuiesceBarrier is an initialized, unsealed StorageBarrier under cfg.
func newQuiesceBarrier(t *testing.T, cfg seal.BarrierConfig) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(quiescePassphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	cfg.RootKeyPath = "encfs-quiesce-root-key"
	b, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(), cfg, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, seal.MustCredentials(ctx, []byte(quiescePassphrase))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

// syncGate blocks the first sync after it is armed until release closes.
type syncGate struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newSyncGate() *syncGate {
	return &syncGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *syncGate) pass() {
	if g.armed.CompareAndSwap(true, false) {
		close(g.entered)
		<-g.release
	}
}

// gatedFS is a base file system whose writable files sync through a gate,
// so a test holds a durable write in flight without sleeping.
type gatedFS struct {
	filesys.LockFS
	gate *syncGate
}

func (fs gatedFS) wrap(f filesys.File, err error) (filesys.File, error) {
	if err != nil {
		return nil, err
	}
	return gatedFile{File: f, gate: fs.gate}, nil
}

func (fs gatedFS) Create(name string) (filesys.File, error) {
	return fs.wrap(fs.LockFS.Create(name))
}

func (fs gatedFS) OpenReadWrite(name string) (filesys.File, error) {
	return fs.wrap(fs.LockFS.OpenReadWrite(name))
}

type gatedFile struct {
	filesys.File
	gate *syncGate
}

func (f gatedFile) Sync() error {
	f.gate.pass()
	return f.File.Sync()
}

// openGatedStore opens a synchronous PebbleStorage over encfs over b, on a
// base file system that syncs through gate.
func openGatedStore(t *testing.T, b *seal.StorageBarrier, gate *syncGate) *pebblestore.PebbleStorage {
	t.Helper()
	cfg := pebblestore.TestConfig("db")
	cfg.SyncWrites = true
	cfg.FS = pebblestore.WrapVFS(vfs.NewMem(), func(base filesys.FS) filesys.FS {
		return encfs.New(gatedFS{LockFS: base.(filesys.LockFS), gate: gate}, b)
	})
	store, err := pebblestore.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func awaitClosed(g *quiesce.Gate) {
	for !g.Closed() {
		runtime.Gosched()
	}
}

func TestFS_QuiesceIsTheBarriersGate(t *testing.T) {
	b := newQuiesceBarrier(t, seal.BarrierConfig{})
	fs := encfs.New(pebblestore.FromVFS(vfs.NewMem()), b).(*encfs.FS)
	if fs.Quiesce() == nil || fs.Quiesce() != b.Quiesce() {
		t.Fatal("Quiesce() is not the barrier's gate")
	}
}

func TestFS_QuiesceIsNilWhenTheBarrierHasNoGate(t *testing.T) {
	fs := encfs.New(pebblestore.FromVFS(vfs.NewMem()), &sealableKEK{}).(*encfs.FS)
	if fs.Quiesce() != nil {
		t.Fatal("Quiesce() returned a gate for a barrier without one")
	}
}

// TestPebbleStorage_SealDrainsADurableWriteInFlight runs the whole path over
// a real StorageBarrier: a synchronous write holds Seal until its WAL sync
// ends, a write started meanwhile refuses with the typed sealed error, and
// unsealing admits writes again.
func TestPebbleStorage_SealDrainsADurableWriteInFlight(t *testing.T) {
	ctx := context.Background()
	b := newQuiesceBarrier(t, seal.BarrierConfig{SealDrainTimeout: time.Minute})
	gate := newSyncGate()
	store := openGatedStore(t, b, gate)
	if err := store.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set k1: %v", err)
	}

	gate.armed.Store(true)
	written := make(chan error, 1)
	go func() { written <- store.Set([]byte("k2"), []byte("v2")) }()
	<-gate.entered
	sealed := make(chan error, 1)
	go func() { sealed <- b.Seal() }()
	awaitClosed(b.Quiesce())
	select {
	case err := <-sealed:
		t.Fatalf("Seal returned %v with a durable write in flight", err)
	default:
	}
	if b.IsSealed() {
		t.Fatal("the barrier sealed with a durable write in flight")
	}
	var refused *pebblestore.SealedError
	if err := store.Set([]byte("k3"), []byte("v3")); !errors.As(err, &refused) {
		t.Fatalf("Set while sealing = %v; want a *SealedError", err)
	}

	close(gate.release)
	if err := <-written; err != nil {
		t.Fatalf("the write in flight = %v", err)
	}
	if err := <-sealed; err != nil {
		t.Fatalf("Seal = %v", err)
	}
	if !b.IsSealed() {
		t.Fatal("IsSealed() = false after Seal")
	}
	if err := store.Set([]byte("k3"), []byte("v3")); !errors.As(err, &refused) {
		t.Fatalf("Set while sealed = %v; want a *SealedError", err)
	}

	if err := b.Unseal(ctx, seal.MustCredentials(ctx, []byte(quiescePassphrase))); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := store.Set([]byte("k3"), []byte("v3")); err != nil {
		t.Fatalf("Set after unseal: %v", err)
	}
	for _, k := range []string{"k1", "k2", "k3"} {
		if _, err := store.Get([]byte(k)); err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestPebbleStorage_SealTimesOutNamingTheStore holds a durable write past
// the drain bound: Seal still seals and names the store that did not drain.
func TestPebbleStorage_SealTimesOutNamingTheStore(t *testing.T) {
	ctx := context.Background()
	b := newQuiesceBarrier(t, seal.BarrierConfig{SealDrainTimeout: time.Millisecond})
	gate := newSyncGate()
	store := openGatedStore(t, b, gate)

	gate.armed.Store(true)
	written := make(chan error, 1)
	go func() { written <- store.Set([]byte("k"), []byte("v")) }()
	<-gate.entered
	err := b.Seal()
	var timeout *quiesce.DrainTimeoutError
	if !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{"db"}) {
		t.Fatalf("Seal = %v; want a DrainTimeoutError naming db", err)
	}
	if !b.IsSealed() {
		t.Fatal("a drain timeout vetoed the seal")
	}
	close(gate.release)
	<-written

	if err := b.Unseal(ctx, seal.MustCredentials(ctx, []byte(quiescePassphrase))); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := store.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set after unseal: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
