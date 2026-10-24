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

package vector

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// openSealableVectorStorage opens a vector store on an in-memory encfs whose
// KEK is a real, unsealed StorageBarrier, with one namespace, and returns the
// store and the barrier.
func openSealableVectorStorage(t *testing.T) (*PebbleVectorStorage, *seal.StorageBarrier) {
	t.Helper()
	b, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	memFS := func(o *pebble.Options) { o.FS = vfs.NewMem() }
	s, err := NewPebbleVectorStorage(DefaultConfig("vectors"), &testLogger{}, memFS, WithEncryptor(atrest.New(b)))
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateNamespace(context.Background(), &NamespaceInfo{Name: "ns", Dimension: 3, CreatedAt: time.Now().UnixNano()}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	return s, b
}

// TestPebbleVectorStorage_SealedBarrierRefusesInsteadOfPanicking drives a
// vector store on encfs across a seal. Every write that may rotate the WAL
// must refuse with ErrBarrierSealed before it reaches Pebble, which panics
// when the barrier refuses the new WAL file.
func TestPebbleVectorStorage_SealedBarrierRefusesInsteadOfPanicking(t *testing.T) {
	ctx := context.Background()
	s, barrier := openSealableVectorStorage(t)
	vec := &VectorValue{ID: 1, Version: 1, Embedding: []float32{1, 2, 3}}
	if err := s.UpsertVector(ctx, "ns", vec); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	writes := map[string]func() error{
		"UpsertVector": func() error {
			return s.UpsertVector(ctx, "ns", &VectorValue{ID: 2, Version: 1, Embedding: []float32{1, 2, 3}})
		},
		"BatchUpsertVectors": func() error {
			return s.BatchUpsertVectors(ctx, "ns", []*VectorValue{{ID: 3, Version: 1, Embedding: []float32{1, 2, 3}}})
		},
		"DeleteVector": func() error { return s.DeleteVector(ctx, "ns", 1) },
		"CreateNamespace": func() error {
			return s.CreateNamespace(ctx, &NamespaceInfo{Name: "other", Dimension: 3})
		},
		"NextID": func() error { _, err := s.NextID("ns"); return err },
		"BatchAllocateIDs": func() error {
			_, err := s.BatchAllocateIDs("ns", 2)
			return err
		},
		"Compact": func() error { return s.Compact(ctx, "ns") },
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, pebblestore.ErrBarrierSealed) {
			t.Errorf("%s under a sealed barrier = %v; want ErrBarrierSealed", name, err)
		}
	}
}

// TestPebbleVectorStorage_SealedIDAllocationSpendsNoID pins that a refused
// allocation leaves the namespace's sequence where it was.
func TestPebbleVectorStorage_SealedIDAllocationSpendsNoID(t *testing.T) {
	s, barrier := openSealableVectorStorage(t)
	first, err := s.NextID("ns")
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if err := barrier.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := s.NextID("ns"); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("NextID under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	if got := s.idAllocators["ns"].Load(); got != first {
		t.Fatalf("sequence after a refused NextID = %d; want %d", got, first)
	}
}
