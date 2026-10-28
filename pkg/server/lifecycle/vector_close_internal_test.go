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

package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// holdingVectorStorage is the node's real vector storage with its Compact
// held: a compaction reports that it began, waits for its context to be
// cancelled or for the test to release it, and only then compacts the real
// storage. Close records whether it ran while that compaction was still in
// flight, then closes the real storage.
type holdingVectorStorage struct {
	vectorstorage.Storage

	entered   chan struct{} // closed when Compact begins
	cancelled chan struct{} // closed when Compact observes its context cancelled
	release   chan struct{} // closed by the test to let Compact finish
	exited    chan struct{} // closed when Compact returns

	closed          atomic.Bool
	closedUnderHeld atomic.Bool
}

var _ vectorstorage.Storage = (*holdingVectorStorage)(nil)

func newHoldingVectorStorage(inner vectorstorage.Storage) *holdingVectorStorage {
	return &holdingVectorStorage{
		Storage:   inner,
		entered:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
		exited:    make(chan struct{}),
	}
}

// systemScopedVectorNamespace is the storage name the vector service gives a
// system-tenant namespace: the unit separator, then the logical name.
func systemScopedVectorNamespace(logical string) string {
	return "\x1f" + logical
}

func (h *holdingVectorStorage) Compact(ctx context.Context, namespace string) error {
	defer close(h.exited)
	close(h.entered)
	select {
	case <-ctx.Done():
		close(h.cancelled)
	case <-h.release:
	}
	<-h.release
	return h.Storage.Compact(ctx, namespace)
}

func (h *holdingVectorStorage) Close() error {
	select {
	case <-h.exited:
	default:
		h.closedUnderHeld.Store(true)
	}
	h.closed.Store(true)
	return h.Storage.Close()
}

// TestNode_StopDrainsVectorJobsBeforeClosingStorage: Stop cancels an async
// vector compaction that is in flight, keeps the vector storage open while
// that compaction has not exited, and returns only once it has, closing the
// storage last (#1171).
func TestNode_StopDrainsVectorJobsBeforeClosingStorage(t *testing.T) {
	ctx := context.Background()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	var held *holdingVectorStorage
	node, err := BuildServices(ctx, Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		wrapVectorStorage: func(s vectorstorage.Storage) vectorstorage.Storage {
			held = newHoldingVectorStorage(s)
			return held
		},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	if held == nil {
		t.Fatal("BuildServices did not compose the vector storage through the seam")
	}
	// A failure below must not leave the compaction, and Stop with it, held.
	var released atomic.Bool
	releaseOnce := func() {
		if released.CompareAndSwap(false, true) {
			close(held.release)
		}
	}
	t.Cleanup(releaseOnce)

	// The namespace is seeded through the storage, not the service: the
	// service's CreateNamespace builds an HNSW index, which a build without
	// qrdb_vector does not have, and compaction needs only the stored
	// namespace. The name is the system tenant's scoped form, which the
	// service looks the namespace up by.
	if err := held.CreateNamespace(ctx, &vectorstorage.NamespaceInfo{
		Name:           systemScopedVectorNamespace("held"),
		Dimension:      3,
		DistanceMetric: "L2",
		IndexType:      "HNSW",
	}); err != nil {
		t.Fatalf("seed namespace: %v", err)
	}
	if _, err := node.Vector.CompactNamespace(ctx, &service.CompactNamespaceRequest{
		Namespace: "held",
		Async:     true,
	}); err != nil {
		t.Fatalf("CompactNamespace: %v", err)
	}
	<-held.entered

	stopped := make(chan error, 1)
	go func() { stopped <- node.Stop(ctx) }()

	// The held compaction exits only once released, so Stop cannot return
	// before its context is cancelled unless shutdown skipped the drain.
	select {
	case <-held.cancelled:
	case err := <-stopped:
		t.Fatalf("Stop returned (err=%v) without cancelling the in-flight compaction", err)
	}
	if held.closed.Load() {
		t.Fatal("vector storage closed while the cancelled compaction had not exited")
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (err=%v) before the in-flight compaction exited", err)
	default:
	}

	releaseOnce()
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	<-held.exited
	if !held.closed.Load() {
		t.Fatal("Stop returned without closing the vector storage")
	}
	if held.closedUnderHeld.Load() {
		t.Fatal("vector storage closed before the compaction exited")
	}
}
