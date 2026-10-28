// Copyright 2026 Jeremy Hahn
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

package pebbleindex_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/index/pebbleindex"
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

// An index over a watched database refuses every write with
// ErrBarrierSealed once the barrier seals, instead of reaching Pebble.
func TestIndex_SealedBarrierRefusesWrites(t *testing.T) {
	db, seals, barrier := openSealableDB(t)
	idx, err := pebbleindex.New(db, "email", false, index.IndexValueTypeString, pebbleindex.WithSealWatch(seals))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := idx.Put([]byte("k1"), []byte("a@example.com")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	writes := map[string]func() error{
		"Put":    func() error { return idx.Put([]byte("k2"), []byte("b@example.com")) },
		"Delete": func() error { return idx.Delete([]byte("k1"), []byte("a@example.com")) },
		"Clear":  idx.Clear,
	}
	for name, write := range writes {
		err := write()
		if !errors.Is(err, pebblestore.ErrBarrierSealed) {
			t.Errorf("%s under a sealed barrier = %v; want ErrBarrierSealed", name, err)
		}
		var sealed *pebblestore.SealedError
		if !errors.As(err, &sealed) {
			t.Errorf("%s under a sealed barrier = %T; want *SealedError", name, err)
		}
	}

	// No refused write landed: the Put added nothing, and neither the
	// Delete nor the Clear removed the entry written before the seal.
	if err := barrier.Unseal(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if got, err := idx.Get([]byte("b@example.com")); err != nil || len(got) != 0 {
		t.Errorf("Get(refused Put's value) = %q, %v; want none", got, err)
	}
	if got, err := idx.Get([]byte("a@example.com")); err != nil || len(got) != 1 || string(got[0]) != "k1" {
		t.Errorf("Get(pre-seal value) = %q, %v; want [k1]", got, err)
	}
}

// The watch passed through NewWithConfig is the one checked, and writes
// succeed again once the barrier is unsealed.
func TestIndex_UnsealedBarrierWritesAgain(t *testing.T) {
	db, seals, barrier := openSealableDB(t)
	idx, err := pebbleindex.NewWithConfig(db, "name", index.IndexConfig{
		ValueType:     index.IndexValueTypeString,
		CaseSensitive: true,
	}, pebbleindex.WithSealWatch(seals))
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := idx.Put([]byte("k1"), []byte("alice")); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Put under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	if err := barrier.Unseal(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := idx.Put([]byte("k1"), []byte("alice")); err != nil {
		t.Fatalf("Put after unseal: %v", err)
	}
	got, err := idx.Get([]byte("alice"))
	if err != nil || len(got) != 1 || string(got[0]) != "k1" {
		t.Fatalf("Get after unseal = %q, %v; want [k1]", got, err)
	}
	if err := idx.Clear(); err != nil {
		t.Fatalf("Clear after unseal: %v", err)
	}
}

// An index built without a watch writes as it always did.
func TestIndex_NoSealWatchWrites(t *testing.T) {
	db, err := pebble.Open("db", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	idx, err := pebbleindex.New(db, "n", true, index.IndexValueTypeString)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := idx.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := idx.Delete([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
