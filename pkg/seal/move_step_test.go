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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

const (
	moveTestNode   = "test-node"
	moveTestTenant = "acme"
)

// moveFixture is a registry over two real "dir" pools on this node,
// "default" and "fast", with tenant moveTestTenant reserved on "default"
// and a nested tree written into its directory.
type moveFixture struct {
	reg         *BarrierRegistry
	catalog     *volume.Catalog
	kv          *sealTestFakeKV
	open        *volume.OpenVolumes
	defaultRoot string
	fastRoot    string
	src         string
	path        string
}

func newMoveFixture(t *testing.T, opts ...Option) *moveFixture {
	t.Helper()
	return newMoveFixtureAt(t, moveTestTenant, opts...)
}

// newMoveFixtureAt is newMoveFixture with the tenant's assignment reserved
// at path in the default pool.
func newMoveFixtureAt(t *testing.T, path string, opts ...Option) *moveFixture {
	t.Helper()
	ctx := context.Background()
	defaultRoot := t.TempDir()
	catalog, open, kv := newTestVolumeFixtureKV(t, defaultRoot)

	fastRoot := t.TempDir()
	provider, err := volume.NewRegistry().Lookup("dir")
	if err != nil {
		t.Fatalf("Lookup(dir): %v", err)
	}
	vol, err := provider.Open(ctx, volume.Spec{Pool: "fast", NodeID: moveTestNode, Path: fastRoot})
	if err != nil {
		t.Fatalf("provider.Open(fast): %v", err)
	}
	t.Cleanup(func() { _ = vol.Close(context.Background()) })
	if err := open.Add("fast", vol); err != nil {
		t.Fatalf("open.Add(fast): %v", err)
	}
	if _, err := catalog.CreatePool(ctx, volume.CreatePoolRequest{Name: "fast", Provider: "dir"}); err != nil {
		t.Fatalf("CreatePool(fast): %v", err)
	}

	opts = append([]Option{WithVolumeAllocator(catalog), WithVolumeResolver(open)}, opts...)
	reg, err := NewBarrierRegistry(poolTestLogger(), newPoolSystemBarrier(t), opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	a, err := catalog.Reserve(ctx, moveTestTenant, "default", path, 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	src, err := reg.RequireInsideLocalPool(ctx, moveTestTenant)
	if err != nil {
		t.Fatalf("RequireInsideLocalPool: %v", err)
	}
	writeMoveTree(t, src)
	return &moveFixture{reg: reg, catalog: catalog, kv: kv, open: open, defaultRoot: defaultRoot, fastRoot: fastRoot, src: src, path: a.Path}
}

func writeMoveTree(t *testing.T, root string) {
	t.Helper()
	big := bytes.Repeat([]byte("0123456789abcdef"), (1<<20)/16+4096)
	files := []struct {
		rel  string
		data []byte
		mode os.FileMode
	}{
		{"MANIFEST-000001", []byte("manifest"), 0o644},
		{"sub/deeper/000002.sst", big, 0o644},
		{"sub/empty.log", nil, 0o644},
		{"secret.key", []byte("private"), 0o600},
	}
	for _, f := range files {
		p := filepath.Join(root, f.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
	}
}

func mustManifest(t *testing.T, root string) *volume.Manifest {
	t.Helper()
	m, err := volume.BuildManifest(context.Background(), root)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	return m
}

// assertNoPaths fails when err's message names either pool root.
func (f *moveFixture) assertNoPaths(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, p := range []string{f.defaultRoot, f.fastRoot} {
		if strings.Contains(err.Error(), p) {
			t.Fatalf("error names a pool path: %v", err)
		}
	}
}

func assertGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination still present (Lstat err %v)", err)
	}
}

func assertStep(t *testing.T, err error, phase string) *volume.MoveStepError {
	t.Helper()
	var se *volume.MoveStepError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a *volume.MoveStepError", err)
	}
	if se.NodeID != moveTestNode {
		t.Fatalf("MoveStepError.NodeID = %q, want %q", se.NodeID, moveTestNode)
	}
	if phase != "" && se.Phase != phase {
		t.Fatalf("MoveStepError.Phase = %q, want %q", se.Phase, phase)
	}
	return se
}

func TestRunMoveCopyStep_Success(t *testing.T) {
	f := newMoveFixture(t)
	before := mustManifest(t, f.src)

	m, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "fast")
	if err != nil {
		t.Fatalf("RunMoveCopyStep: %v", err)
	}
	dst := filepath.Join(f.fastRoot, f.path)
	if !reflect.DeepEqual(m, before) {
		t.Fatalf("returned manifest differs from the source manifest")
	}
	if got := mustManifest(t, dst); !reflect.DeepEqual(got, m) {
		t.Fatalf("destination manifest differs from the returned manifest")
	}
	big := filepath.Join("sub", "deeper", "000002.sst")
	a, _ := os.ReadFile(filepath.Join(f.src, big))
	b, _ := os.ReadFile(filepath.Join(dst, big))
	if len(a) <= 1<<20 || !bytes.Equal(a, b) {
		t.Fatalf("large file bytes differ (src %d, dst %d)", len(a), len(b))
	}
	fi, err := os.Stat(filepath.Join(dst, "secret.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret.key mode = %v, err %v; want 0600", fi, err)
	}
	if after := mustManifest(t, f.src); !reflect.DeepEqual(after, before) {
		t.Fatalf("source changed")
	}
	a2, err := f.catalog.Resolve(context.Background(), moveTestTenant)
	if err != nil || a2.Pool != "default" {
		t.Fatalf("assignment = %+v, err %v; want pool default", a2, err)
	}
}

func TestRunMoveCopyStep_LeftoverDestinationRedone(t *testing.T) {
	f := newMoveFixture(t)
	dst := filepath.Join(f.fastRoot, f.path)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "fast")
	if err != nil {
		t.Fatalf("RunMoveCopyStep: %v", err)
	}
	assertGone(t, filepath.Join(dst, "stray"))
	if got := mustManifest(t, dst); !reflect.DeepEqual(got, m) {
		t.Fatalf("destination manifest differs after redo")
	}
}

func TestRunMoveCopyStep_ArgumentRefusals(t *testing.T) {
	f := newMoveFixture(t)
	ctx := context.Background()
	cases := []struct {
		name, node, tenant, pool string
		want                     error
	}{
		{"empty tenant", moveTestNode, "", "fast", ErrEmptyTenantID},
		{"empty node", "", moveTestTenant, "fast", ErrMoveStepInvalid},
		{"empty pool", moveTestNode, moveTestTenant, "", ErrMoveStepInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.reg.RunMoveCopyStep(ctx, tc.node, tc.tenant, tc.pool); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	bare, err := NewBarrierRegistry(poolTestLogger(), newPoolSystemBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := bare.RunMoveCopyStep(ctx, moveTestNode, moveTestTenant, "fast"); !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("no pools: err = %v, want ErrPoolNotConfigured", err)
	}
}

func TestRunMoveCopyStep_NoAssignment(t *testing.T) {
	f := newMoveFixture(t)
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, "nobody", "fast")
	assertStep(t, err, "copy")
	var nf *volume.AssignmentNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *volume.AssignmentNotFoundError", err)
	}
	f.assertNoPaths(t, err)
}

func TestRunMoveCopyStep_SamePool(t *testing.T) {
	f := newMoveFixture(t)
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "default")
	assertStep(t, err, "copy")
	if !errors.Is(err, ErrMoveSamePool) {
		t.Fatalf("err = %v, want ErrMoveSamePool", err)
	}
}

func TestRunMoveCopyStep_PoolNotOpenHere(t *testing.T) {
	f := newMoveFixture(t)
	if _, err := f.catalog.CreatePool(context.Background(), volume.CreatePoolRequest{Name: "cold", Provider: "dir"}); err != nil {
		t.Fatalf("CreatePool(cold): %v", err)
	}
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "cold")
	assertStep(t, err, "copy")
	var pu *volume.PoolUnavailableError
	if !errors.As(err, &pu) {
		t.Fatalf("err = %v, want *volume.PoolUnavailableError", err)
	}
	f.assertNoPaths(t, err)
}

func TestRunMoveCopyStep_TenantOpen(t *testing.T) {
	f := newMoveFixture(t)
	f.reg.tenants.Store(moveTestTenant, WrapTenantBarrier(moveTestTenant, newPoolSystemBarrier(t)))
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "fast")
	assertStep(t, err, "copy")
	if !errors.Is(err, ErrMoveTenantOpen) {
		t.Fatalf("err = %v, want ErrMoveTenantOpen", err)
	}
	assertGone(t, filepath.Join(f.fastRoot, f.path))
}

func TestRunMoveCopyStep_SymlinkInSource(t *testing.T) {
	f := newMoveFixture(t)
	if err := os.Symlink("MANIFEST-000001", filepath.Join(f.src, "sub", "link")); err != nil {
		t.Fatal(err)
	}
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "fast")
	assertStep(t, err, "copy")
	assertGone(t, filepath.Join(f.fastRoot, f.path))
	f.assertNoPaths(t, err)
}

func truncateOnCopy(t *testing.T, reg *BarrierRegistry) {
	t.Helper()
	reg.moveAfterCopy = func(dst string) {
		if err := os.Truncate(filepath.Join(dst, "MANIFEST-000001"), 1); err != nil {
			t.Errorf("Truncate: %v", err)
		}
	}
}

func TestRunMoveCopyStep_VerifyFailureRemovesDestination(t *testing.T) {
	f := newMoveFixture(t)
	before := mustManifest(t, f.src)
	truncateOnCopy(t, f.reg)
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "fast")
	assertStep(t, err, "verify")
	var mm *volume.ManifestMismatchError
	if !errors.As(err, &mm) || mm.Reason != "size" {
		t.Fatalf("err = %v, want *volume.ManifestMismatchError Reason size", err)
	}
	assertGone(t, filepath.Join(f.fastRoot, f.path))
	if after := mustManifest(t, f.src); !reflect.DeepEqual(after, before) {
		t.Fatalf("source changed")
	}
	f.assertNoPaths(t, err)
}

// failingRemover is an OS remover whose RemoveAll always fails.
type failingRemover struct {
	osDirectoryRemover
	err error
}

func (d failingRemover) RemoveAll(string) error { return d.err }

func TestRunMoveCopyStep_VerifyFailureRemoveFails(t *testing.T) {
	removeErr := errors.New("remove refused")
	f := newMoveFixture(t, WithDirectoryRemover(failingRemover{err: removeErr}))
	truncateOnCopy(t, f.reg)
	_, err := f.reg.RunMoveCopyStep(context.Background(), moveTestNode, moveTestTenant, "fast")
	assertStep(t, err, "verify")
	if !errors.Is(err, removeErr) {
		t.Fatalf("err = %v, want it to wrap the remover's error", err)
	}
	f.assertNoPaths(t, err)
}

func TestRunMoveCopyStep_CancelledContext(t *testing.T) {
	f := newMoveFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.reg.RunMoveCopyStep(ctx, moveTestNode, moveTestTenant, "fast")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertGone(t, filepath.Join(f.fastRoot, f.path))
	f.assertNoPaths(t, err)
}
