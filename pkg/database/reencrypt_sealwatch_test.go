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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newSealableBarrier returns an initialized, unsealed none-strategy barrier.
func newSealableBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(reencTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

// TestCommitWatched_SealedBarrierRefusesInsteadOfPanicking drives the
// destination writes ReencryptPebbleStore makes, a synced batch commit and
// the closing flush, on encfs across a seal: each refuses with
// ErrBarrierSealed before it reaches Pebble, which panics when the barrier
// refuses the WAL file a flush or memtable rotation creates.
func TestCommitWatched_SealedBarrierRefusesInsteadOfPanicking(t *testing.T) {
	barrier := newSealableBarrier(t)
	opts := &pebble.Options{FS: vfs.NewMem()}
	atrest.New(barrier).Apply(opts)
	dst, seals, err := pebblestore.Open("dst", opts, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = dst.Close() }()

	batch := dst.NewBatch()
	defer func() { _ = batch.Close() }()
	if err := batch.Set([]byte("k"), []byte("v"), nil); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := commitWatched(seals, batch); err != nil {
		t.Fatalf("commitWatched under an unsealed barrier: %v", err)
	}

	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealedBatch := dst.NewBatch()
	defer func() { _ = sealedBatch.Close() }()
	if err := sealedBatch.Set([]byte("k2"), []byte("v"), nil); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := commitWatched(seals, sealedBatch); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("commitWatched under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	if err := seals.Flush(dst); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Flush under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
}

// TestReencryptPebbleStore_SealedDestinationBarrierFailsTyped pins that a
// re-encryption into a store whose barrier is sealed ends with a typed error
// rather than a panic.
func TestReencryptPebbleStore_SealedDestinationBarrierFailsTyped(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir() + "/src"
	srcEnc := newNoneEncryptor(t, "src-root-key")
	writeStore(t, srcDir, srcEnc, map[string]string{"a": "1", "b": "2"})

	dstBarrier := newSealableBarrier(t)
	if err := dstBarrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, err := ReencryptPebbleStore(ctx, srcDir, srcEnc, t.TempDir()+"/dst", atrest.New(dstBarrier))
	var openErr *ReencryptOpenError
	var copyErr *ReencryptCopyError
	if !errors.As(err, &openErr) && !errors.As(err, &copyErr) {
		t.Fatalf("ReencryptPebbleStore into a sealed barrier = %v; want a typed open or copy error", err)
	}
}
