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
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// mountPointRemover is the real filesystem, except that the directories it is
// told are mount points refuse their own rmdir exactly as the kernel does for
// a mount point: EBUSY on the mount directory itself, while everything inside
// it is removable. rmdirErr overrides the errno (EXDEV covers the rename-based
// variants some filesystems report).
type mountPointRemover struct {
	mu        sync.Mutex
	mounts    map[string]bool
	rmdirErr  error
	failAll   error // when set, RemoveAll fails with it
	removeAll []string
}

func (m *mountPointRemover) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }

func (m *mountPointRemover) RemoveAll(path string) error {
	m.mu.Lock()
	m.removeAll = append(m.removeAll, path)
	fail := m.failAll
	m.mu.Unlock()
	if fail != nil {
		return fail
	}
	return os.RemoveAll(path)
}

func (m *mountPointRemover) Remove(path string) error {
	m.mu.Lock()
	mount := m.mounts[path]
	m.mu.Unlock()
	if mount {
		errno := m.rmdirErr
		if errno == nil {
			errno = syscall.EBUSY
		}
		return &os.PathError{Op: "remove", Path: path, Err: errno}
	}
	return os.Remove(path)
}

// TestRemoveDataRoot_MountPointIsReclaimedOnceEmptied: a dedicated DataRoot
// that is a mount point cannot itself be removed. Removing the CONTENTS and
// accepting an emptied root whose rmdir fails with EBUSY or EXDEV is what
// reclaims it; RemoveAll alone fails on the final rmdir, and a reclamation
// that keeps its records until removal succeeds would retry it forever.
func TestRemoveDataRoot_MountPointIsReclaimedOnceEmptied(t *testing.T) {
	for name, errno := range map[string]error{"EBUSY": syscall.EBUSY, "EXDEV": syscall.EXDEV} {
		t.Run(name, func(t *testing.T) {
			rm := &mountPointRemover{mounts: map[string]bool{}, rmdirErr: errno}
			reg, _ := newPooledTestRegistry(t, WithDirectoryRemover(rm))
			root := filepath.Join(poolBase(t, reg), "mnt")
			rm.mounts[root] = true
			if err := os.MkdirAll(filepath.Join(root, "barrier"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "databases"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := reg.RemoveDataRoot(context.Background(), root); err != nil {
				t.Fatalf("RemoveDataRoot on an emptied mount point = %v; want reclaimed", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("mount point contents = %v, %v; want emptied", entries, err)
			}
		})
	}
}

// TestRemoveDataRoot_OtherRmdirFailuresAreReported: only the mount-point
// errnos count as reclaimed; any other rmdir failure is reported.
func TestRemoveDataRoot_OtherRmdirFailuresAreReported(t *testing.T) {
	ctx := context.Background()
	rm := &mountPointRemover{mounts: map[string]bool{}, rmdirErr: syscall.EACCES}
	reg, _ := newPooledTestRegistry(t, WithDirectoryRemover(rm))
	base := poolBase(t, reg)
	root := filepath.Join(base, "d")
	rm.mounts[root] = true
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := reg.RemoveDataRoot(ctx, root); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("RemoveDataRoot = %v; want the EACCES", err)
	}
	// An absent root is already reclaimed.
	if err := reg.RemoveDataRoot(ctx, filepath.Join(base, "never")); err != nil {
		t.Fatalf("RemoveDataRoot(absent) = %v; want nil", err)
	}
	// A regular file where the root should be is removed as the path it is.
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.RemoveDataRoot(ctx, file); err != nil {
		t.Fatalf("RemoveDataRoot(file) = %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("file survived: %v", err)
	}
}

// TestDestroyTenant_FailedRemovalKeepsTheRecords pins DestroyTenant's order:
// the DataRoot is removed before the records that name it are dropped, so a
// failed removal leaves the record and a retried DestroyTenant finishes the
// job. No test pinned this order before.
func TestDestroyTenant_FailedRemovalKeepsTheRecords(t *testing.T) {
	ctx := context.Background()
	rm := &mountPointRemover{mounts: map[string]bool{}, failAll: syscall.EIO}
	reg, system := newPooledTestRegistry(t, WithDirectoryRemover(rm))
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatal(err)
	}

	err = reg.DestroyTenant(ctx, "acme")
	var re *TenantDataRootRemoveError
	if !errors.As(err, &re) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("DestroyTenant = %v; want *TenantDataRootRemoveError", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); err != nil {
		t.Fatalf("the record naming the DataRoot was dropped although its removal failed: %v", err)
	}

	rm.mu.Lock()
	rm.failAll = nil
	rm.mu.Unlock()
	if err := reg.DestroyTenant(ctx, "acme"); err != nil && !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("retried DestroyTenant = %v", err)
	}
	if _, err := os.Stat(dataRoot); !os.IsNotExist(err) {
		t.Fatalf("the retry did not remove the DataRoot: %v", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("the record survived the successful retry: %v", err)
	}
}

// poolBase returns the single pool path of a newPooledTestRegistry registry.
func poolBase(t *testing.T, reg *BarrierRegistry) string {
	t.Helper()
	bases, err := reg.PoolBasePaths(context.Background())
	if err != nil || len(bases) != 1 {
		t.Fatalf("PoolBasePaths = %v, %v; want one pool path", bases, err)
	}
	return bases[0]
}

// TestRemoveDataRoot_RefusesAPathOutsideThisNodesPool: removal acts only
// strictly inside one of this node's own pool paths. A directory anywhere else
// -- another node's pool on a shared host, the pool root itself, or a path that
// escapes with ".." -- is refused with *DataRootOutsideLocalRootsError and left
// exactly as it was, whatever record or caller named it.
func TestRemoveDataRoot_RefusesAPathOutsideThisNodesPool(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)
	base := poolBase(t, reg)

	foreign := filepath.Join(t.TempDir(), "another-node", "tenant")
	if err := os.MkdirAll(filepath.Join(foreign, "barrier"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		foreign,
		base,
		filepath.Join(base, "tenant") + "/../../escape",
	} {
		err := reg.RemoveDataRoot(ctx, path)
		var outside *DataRootOutsideLocalRootsError
		if !errors.As(err, &outside) || outside.DataRoot != path {
			t.Fatalf("RemoveDataRoot(%s) = %v; want *DataRootOutsideLocalRootsError", path, err)
		}
		if outside.Error() == "" {
			t.Fatal("empty message")
		}
	}
	if _, err := os.Stat(filepath.Join(foreign, "barrier")); err != nil {
		t.Fatalf("the refused foreign directory was touched: %v", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the refused pool root was touched: %v", err)
	}
}
