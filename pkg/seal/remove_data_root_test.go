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
			root := filepath.Join(t.TempDir(), "mnt")
			if err := os.MkdirAll(filepath.Join(root, "barrier"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "databases"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			rm := &mountPointRemover{mounts: map[string]bool{root: true}, rmdirErr: errno}
			reg, _ := newPooledTestRegistry(t, WithDirectoryRemover(rm))

			if err := reg.RemoveDataRoot(root); err != nil {
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
	root := filepath.Join(t.TempDir(), "d")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	rm := &mountPointRemover{mounts: map[string]bool{root: true}, rmdirErr: syscall.EACCES}
	reg, _ := newPooledTestRegistry(t, WithDirectoryRemover(rm))
	if err := reg.RemoveDataRoot(root); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("RemoveDataRoot = %v; want the EACCES", err)
	}
	// An absent root is already reclaimed.
	if err := reg.RemoveDataRoot(filepath.Join(t.TempDir(), "never")); err != nil {
		t.Fatalf("RemoveDataRoot(absent) = %v; want nil", err)
	}
	// A regular file where the root should be is removed as the path it is.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.RemoveDataRoot(file); err != nil {
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
	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", filepath.Join(t.TempDir(), "disk"), true)
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

// listFailingBackend is a real memory backend whose List can be made to fail.
type listFailingBackend struct {
	*MemoryBackend
	failList error
}

func (b *listFailingBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if b.failList != nil {
		return nil, b.failList
	}
	return b.MemoryBackend.List(ctx, prefix)
}

// TestRefuseClaimedRoot_ListFailureIsTyped: a registration that cannot read
// the records it must check refuses, with a typed error -- never proceeds as
// if nothing claimed the root.
func TestRefuseClaimedRoot_ListFailureIsTyped(t *testing.T) {
	ctx := context.Background()
	base := &listFailingBackend{MemoryBackend: NewMemoryBackend()}
	system, err := NewStorageBarrier(testLogger(), base, BarrierConfig{RootKeyPath: "system/root-key"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Initialize(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("store unavailable")
	base.failList = boom
	err = reg.refuseClaimedRoot(ctx, "acme", "/srv/disk")
	var ce *ClaimedRootCheckError
	if !errors.As(err, &ce) || ce.TenantID != "acme" || !errors.Is(err, boom) {
		t.Fatalf("refuseClaimedRoot = %v; want *ClaimedRootCheckError wrapping the list failure", err)
	}
	if ce.Error() == "" {
		t.Fatal("empty message")
	}
}

// TestRefuseClaimedRoot_SealedSystemBarrierHasNoRecordsToCheck: with the
// system barrier sealed no record is readable, so there is nothing to refuse
// on -- the same answer the registration's own record write gives.
func TestRefuseClaimedRoot_SealedSystemBarrierHasNoRecordsToCheck(t *testing.T) {
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{RootKeyPath: "system/root-key"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.refuseClaimedRoot(context.Background(), "acme", "/srv/disk"); err != nil {
		t.Fatalf("refuseClaimedRoot with a sealed system barrier = %v; want nil", err)
	}
}
