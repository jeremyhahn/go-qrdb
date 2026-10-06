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

package seal

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// ---------------------------------------------------------------------------
// In-memory KVServicer/TransactionServicer doubles backing a REAL
// volume.Catalog in these tests. The thing under test in the pool-related
// BarrierRegistry tests is BarrierRegistry's own directory/Pebble handling
// (DestroyTenant removing a real directory, ReleaseTenant closing a real
// Pebble, etc.), not the Catalog's own replicated persistence -- a
// single-node, non-replicated KV is a faithful enough backing store for
// that, and pkg/volume's own tests already cover the Catalog's CAS
// mechanics in isolation.
// ---------------------------------------------------------------------------

type sealTestFakeKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newSealTestFakeKV() *sealTestFakeKV { return &sealTestFakeKV{data: make(map[string][]byte)} }

func (f *sealTestFakeKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	return bytes.Clone(v), nil
}

func (f *sealTestFakeKV) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = bytes.Clone(value)
	return nil
}

func (f *sealTestFakeKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.data[key]; exists {
		return false, nil
	}
	f.data[key] = bytes.Clone(value)
	return true, nil
}

func (f *sealTestFakeKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	f.data[key] = bytes.Clone(next)
	return true, nil
}

func (f *sealTestFakeKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	delete(f.data, key)
	return true, nil
}

func (f *sealTestFakeKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]byte)
	for k, v := range f.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = bytes.Clone(v)
		}
	}
	return out, nil
}

// sealTestFakeTx is an in-memory TransactionServicer double that evaluates
// lock.Compare against a shared sealTestFakeKV, tracking a per-key version
// that increments on every write and resets to 0 on delete -- mirroring
// pkg/volume's own catalog_fakes_test.go fakeTx, duplicated here rather
// than imported because it is an unexported test type in another package.
type sealTestFakeTx struct {
	kv *sealTestFakeKV

	mu       sync.Mutex
	versions map[string]int64
}

func newSealTestFakeTx(kv *sealTestFakeKV) *sealTestFakeTx {
	return &sealTestFakeTx{kv: kv, versions: make(map[string]int64)}
}

func (t *sealTestFakeTx) Execute(ctx context.Context, compares []lock.Compare, successOps, failureOps []lock.Op) (*lock.TransactionResponse, error) {
	for _, c := range compares {
		ok, err := t.evalCompare(ctx, c)
		if err != nil {
			return nil, err
		}
		if !ok {
			if err := t.apply(ctx, failureOps); err != nil {
				return nil, err
			}
			return &lock.TransactionResponse{Succeeded: false}, nil
		}
	}
	if err := t.apply(ctx, successOps); err != nil {
		return nil, err
	}
	return &lock.TransactionResponse{Succeeded: true}, nil
}

func (t *sealTestFakeTx) evalCompare(ctx context.Context, c lock.Compare) (bool, error) {
	key := string(c.Key)
	switch c.Target {
	case lock.CompareVersion:
		t.mu.Lock()
		v := t.versions[key]
		t.mu.Unlock()
		return v == c.Version, nil
	case lock.CompareValue:
		cur, err := t.kv.Get(ctx, key)
		if err != nil {
			return false, err
		}
		return bytes.Equal(cur, c.Value), nil
	default:
		return false, nil
	}
}

func (t *sealTestFakeTx) apply(ctx context.Context, ops []lock.Op) error {
	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case lock.OpPut:
			if err := t.kv.Put(ctx, key, op.Value); err != nil {
				return err
			}
			t.mu.Lock()
			t.versions[key]++
			t.mu.Unlock()
		case lock.OpDelete:
			t.kv.mu.Lock()
			delete(t.kv.data, key)
			t.kv.mu.Unlock()
			t.mu.Lock()
			t.versions[key] = 0
			t.mu.Unlock()
		}
	}
	return nil
}

// newTestVolumeFixture opens a REAL "dir" provider volume rooted at a fresh
// t.TempDir(), registers it in both a volume.Catalog (so Reserve/Resolve
// work against a real "default" pool record) and a volume.OpenVolumes (so
// VolumeResolver.Root resolves it locally), and returns both plus the
// catalog's pool name for WithVolumeAllocator/WithVolumeResolver.
func newTestVolumeFixture(t *testing.T) (catalog *volume.Catalog, open *volume.OpenVolumes, poolRoot string) {
	t.Helper()
	poolRoot = t.TempDir()
	catalog, open = newTestVolumeFixtureAt(t, poolRoot)
	return catalog, open, poolRoot
}

// newTestVolumeFixtureAt is newTestVolumeFixture pinned to a caller-chosen
// poolRoot, rather than a fresh t.TempDir(): a guard test proving
// RequirePathInsideLocalPools' containment math needs an exact pool path
// shape (a sibling sharing a prefix, a trailing separator), which only the
// caller can construct. poolRoot is created if it does not already exist --
// an operator provisions the mount before a pool is ever opened on it, and
// this fixture stands in for that step so the caller only has to name the
// path, not pre-create it.
func newTestVolumeFixtureAt(t *testing.T, poolRoot string) (catalog *volume.Catalog, open *volume.OpenVolumes) {
	t.Helper()
	ctx := context.Background()

	if err := os.MkdirAll(poolRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", poolRoot, err)
	}

	registry := volume.NewRegistry()
	provider, err := registry.Lookup("dir")
	if err != nil {
		t.Fatalf("registry.Lookup(dir): %v", err)
	}

	vol, err := provider.Open(ctx, volume.Spec{Pool: "default", NodeID: "test-node", Path: poolRoot})
	if err != nil {
		t.Fatalf("provider.Open: %v", err)
	}
	t.Cleanup(func() { _ = vol.Close(context.Background()) })

	open = volume.NewOpenVolumes("test-node")
	if err := open.Add("default", vol); err != nil {
		t.Fatalf("open.Add: %v", err)
	}

	kv := newSealTestFakeKV()
	tx := newSealTestFakeTx(kv)
	catalog, err = volume.NewCatalog(kv, tx, registry)
	if err != nil {
		t.Fatalf("volume.NewCatalog: %v", err)
	}
	if _, err := catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "default", Provider: "dir"}); err != nil {
		t.Fatalf("catalog.CreatePool: %v", err)
	}
	return catalog, open
}

// aliasVolume reports reportedRoot from Root() while delegating everything
// else to the real volume.Volume it wraps. The "dir" provider refuses to
// Open a pool path that is itself a symlink (validation.RejectSymlinkedPath)
// -- an operator's mount alias is exactly that -- so this double stands in
// for a provider that permits one, which is what
// RequirePathInsideLocalPools' own resolution of a symlinked pool base
// (resolvedBase, in remove_data_root.go) defends against. Root() reporting
// an absolute, clean path that merely traverses a symlink does not violate
// the Volume contract (volume.Volume.Root's doc requires "absolute, clean",
// never "symlink-free"); only the one shipped provider is stricter than the
// interface requires.
type aliasVolume struct {
	volume.Volume
	root string
}

func (a *aliasVolume) Root() string { return a.root }

// newTestVolumeFixtureAtAlias is newTestVolumeFixtureAt, but the pool's
// Volume reports reportedRoot from Root() instead of realRoot, the path its
// contents are actually opened and written at (see aliasVolume).
func newTestVolumeFixtureAtAlias(t *testing.T, realRoot, reportedRoot string) (catalog *volume.Catalog, open *volume.OpenVolumes) {
	t.Helper()
	ctx := context.Background()

	if err := os.MkdirAll(realRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", realRoot, err)
	}

	registry := volume.NewRegistry()
	provider, err := registry.Lookup("dir")
	if err != nil {
		t.Fatalf("registry.Lookup(dir): %v", err)
	}

	vol, err := provider.Open(ctx, volume.Spec{Pool: "default", NodeID: "test-node", Path: realRoot})
	if err != nil {
		t.Fatalf("provider.Open: %v", err)
	}
	t.Cleanup(func() { _ = vol.Close(context.Background()) })

	open = volume.NewOpenVolumes("test-node")
	if err := open.Add("default", &aliasVolume{Volume: vol, root: reportedRoot}); err != nil {
		t.Fatalf("open.Add: %v", err)
	}

	kv := newSealTestFakeKV()
	tx := newSealTestFakeTx(kv)
	catalog, err = volume.NewCatalog(kv, tx, registry)
	if err != nil {
		t.Fatalf("volume.NewCatalog: %v", err)
	}
	if _, err := catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "default", Provider: "dir"}); err != nil {
		t.Fatalf("catalog.CreatePool: %v", err)
	}
	return catalog, open
}
