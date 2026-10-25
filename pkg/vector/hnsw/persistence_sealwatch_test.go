//go:build qrdb_vector

// Copyright (c) 2026 Jeremy Hahn
// Copyright (c) 2026 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// openSealableDB opens a Pebble database on an encfs filesystem over an
// initialized, unsealed barrier, watched as pebble.Open watches it.
func openSealableDB(t *testing.T) (*pebble.DB, *pebblestore.SealWatch, *seal.StorageBarrier) {
	t.Helper()
	b, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	opts := &pebble.Options{FS: vfs.NewMem()}
	atrest.New(b).Apply(opts)
	db, seals, err := pebblestore.Open("db", opts, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if b.IsSealed() {
			return // Close flushes, which a sealed barrier refuses.
		}
		_ = db.Close()
	})
	return db, seals, b
}

func newSealTestIndex(t *testing.T) *HNSWIndex {
	t.Helper()
	idx, err := NewHNSWIndex(nil)
	if err != nil {
		t.Fatalf("NewHNSWIndex: %v", err)
	}
	for i := uint64(1); i <= 4; i++ {
		if err := idx.Insert(i, []float32{float32(i), 1, 2}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	return idx
}

// Save over a watched database refuses with the watch's *SealedError once
// the barrier seals, and commits nothing.
func TestSave_SealedBarrierRefuses(t *testing.T) {
	db, seals, barrier := openSealableDB(t)
	idx := newSealTestIndex(t)
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	err := idx.Save(db, WithSealWatch(seals))
	if !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Save under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	var sealed *pebblestore.SealedError
	if !errors.As(err, &sealed) {
		t.Fatalf("Save under a sealed barrier = %T; want *SealedError", err)
	}
}

// Save succeeds once the barrier is unsealed, and the saved index loads.
func TestSave_UnsealedBarrierSaves(t *testing.T) {
	db, seals, barrier := openSealableDB(t)
	idx := newSealTestIndex(t)
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := idx.Save(db, WithSealWatch(seals)); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Save under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	if err := barrier.Unseal(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := idx.Save(db, WithSealWatch(seals)); err != nil {
		t.Fatalf("Save after unseal: %v", err)
	}
	loaded, err := NewHNSWIndex(nil)
	if err != nil {
		t.Fatalf("NewHNSWIndex: %v", err)
	}
	if err := loaded.Load(db); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.Count(), idx.Count(); got != want {
		t.Fatalf("loaded Size = %d; want %d", got, want)
	}
}
