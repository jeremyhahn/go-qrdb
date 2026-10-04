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

package securefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// TestWriteFileAtomic_DoesNotSucceedUntilTheDirectoryIsSynced.
//
// rename gives a concurrent reader the old file or the new one and never a
// mix. It says nothing about whether the new DIRECTORY ENTRY survives a power
// loss: that entry is directory metadata and is not on stable storage until
// the directory is fsynced. Without the sync, a crash can recover a set of
// files in an order the writer never wrote them in.
//
// The post-state is byte-identical whether or not the directory was synced,
// so this observes the OPERATION rather than the result: the sync is failed
// at its boundary, and the write must report that failure instead of
// returning success. A test that inspected the file afterwards would pass
// either way. Moved from pkg/clustertls with WriteFileAtomic (go-qrdb#580).
func TestWriteFileAtomic_DoesNotSucceedUntilTheDirectoryIsSynced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "durable.pem")

	sentinel := errors.New("directory sync refused")
	var syncedDirs []string
	t.Cleanup(fsync.SetForTest(func(d string) error {
		syncedDirs = append(syncedDirs, d)
		return sentinel
	}))

	err := WriteFileAtomic(path, []byte("payload"), 0o600)
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteFileAtomic = %v; want the directory-sync failure. Returning success "+
			"before the directory entry is durable is what lets a crash recover files in an "+
			"order they were never written in", err)
	}
	if len(syncedDirs) != 1 || syncedDirs[0] != dir {
		t.Errorf("synced dirs = %v; want exactly [%s] — the containing directory", syncedDirs, dir)
	}
}

// TestWriteFileAtomic_SyncsTheDirectoryAfterEverySuccessfulWrite: the sync is
// not conditional on the file being new. An overwrite replaces the directory
// entry too, so it needs the same durability.
func TestWriteFileAtomic_SyncsTheDirectoryAfterEverySuccessfulWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "durable.pem")

	var count int
	// Count only; do not call fsync.Dir here -- Dir routes through the very
	// seam this replaces, so calling it would recurse until the stack blows.
	// The sync itself is exercised unseamed in pkg/util/fsync's own tests.
	t.Cleanup(fsync.SetForTest(func(string) error { count++; return nil }))

	for i := 0; i < 3; i++ {
		if err := WriteFileAtomic(path, []byte{byte(i)}, 0o600); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if count != 3 {
		t.Errorf("directory synced %d times over 3 writes; want 3", count)
	}
	if got, err := os.ReadFile(path); err != nil || len(got) != 1 || got[0] != 2 {
		t.Fatalf("final content = %v err=%v; want the last write", got, err)
	}
}

// TestWriteFileAtomic_OverwritesModeOfAPreExistingFile verifies the mode of
// the FINAL file is perm, even when a file already occupied the path under a
// different mode -- WriteFileAtomic replaces the inode wholesale, so the old
// file's permissions never survive the rename.
func TestWriteFileAtomic_OverwritesModeOfAPreExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")

	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", fi.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("content = %q err=%v, want \"new\"", got, err)
	}
}

// TestWriteFileAtomic_RefusesASymlinkedTarget verifies a symlink occupying
// path is refused with a typed error rather than replaced.
func TestWriteFileAtomic_RefusesASymlinkedTarget(t *testing.T) {
	dir := t.TempDir()
	linkTarget := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(linkTarget, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatalf("seed link target: %v", err)
	}
	path := filepath.Join(dir, "link")
	if err := os.Symlink(linkTarget, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	err := WriteFileAtomic(path, []byte("payload"), 0o600)
	var symErr *SymlinkRefusedError
	if !errors.As(err, &symErr) {
		t.Fatalf("WriteFileAtomic on a symlinked target = %v, want *SymlinkRefusedError", err)
	}
	if symErr.Path != path {
		t.Errorf("SymlinkRefusedError.Path = %q, want %q", symErr.Path, path)
	}

	// The symlink itself, and what it points to, are both untouched.
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("Lstat(path) after refusal: %v, want the symlink still there", err)
	}
	got, err := os.ReadFile(linkTarget)
	if err != nil || string(got) != "do-not-touch" {
		t.Fatalf("link target content = %q err=%v, want untouched \"do-not-touch\"", got, err)
	}

	// No temp file survives the refusal, since none was ever created.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 { // elsewhere + link
		t.Errorf("directory has %d entries after refusal, want 2 (no temp file leaked)", len(entries))
	}
}

// TestWriteFileAtomic_DanglingSymlinkRefused verifies a symlink whose target
// does not exist is still refused -- the check is "is path a symlink", not
// "does path resolve".
func TestWriteFileAtomic_DanglingSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	var symErr *SymlinkRefusedError
	if err := WriteFileAtomic(path, []byte("x"), 0o600); !errors.As(err, &symErr) {
		t.Fatalf("WriteFileAtomic on a dangling symlink = %v, want *SymlinkRefusedError", err)
	}
}

// TestWriteFileAtomic_NoTempFileSurvivesAFailedWrite verifies a mid-write
// failure (here: the directory sync) leaves no temp file behind.
func TestWriteFileAtomic_NoTempFileSurvivesAFailedWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")

	t.Cleanup(fsync.SetForTest(func(string) error { return errors.New("sync refused") }))

	if err := WriteFileAtomic(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected the directory-sync failure to propagate")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// fsync.Rename's own os.Rename already succeeded before the directory
	// sync is attempted, so the real file lands at path; nothing with the
	// temp-file naming pattern (".target.tmp-*") should remain beside it.
	for _, e := range entries {
		if e.Name() != "target" {
			t.Errorf("unexpected leftover entry %q after a failed write", e.Name())
		}
	}
}

func TestCreateExclusive_WritesWithExactModeAndContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")

	if err := CreateExclusive(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("CreateExclusive: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", fi.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "payload" {
		t.Fatalf("content = %q err=%v, want \"payload\"", got, err)
	}
}

// TestCreateExclusive_RefusesAnExistingFile verifies CreateExclusive never
// overwrites a pre-existing plain file.
func TestCreateExclusive_RefusesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var existsErr *FileExistsError
	if err := CreateExclusive(path, []byte("payload"), 0o600); !errors.As(err, &existsErr) {
		t.Fatalf("CreateExclusive over an existing file = %v, want *FileExistsError", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("content = %q err=%v, want untouched \"original\"", got, err)
	}
}

// TestCreateExclusive_RefusesAnExistingSymlink verifies CreateExclusive
// refuses a symlink occupying the target path, dangling or not: O_EXCL on a
// symlinked pathname fails EEXIST regardless of whether the link resolves.
func TestCreateExclusive_RefusesAnExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	var existsErr *FileExistsError
	if err := CreateExclusive(path, []byte("payload"), 0o600); !errors.As(err, &existsErr) {
		t.Fatalf("CreateExclusive over a symlink = %v, want *FileExistsError", err)
	}
}

// TestCreateExclusive_DirectorySyncFailureLeavesTheCompletedFile verifies
// that a failure of the trailing directory fsync is reported as an error
// without removing the file: by that point the file is already fully
// written and closed, so "not yet durable" is reported honestly rather than
// masked by deleting data that was in fact written -- the same contract
// WriteFileAtomic has for the identical failure (see
// TestWriteFileAtomic_DoesNotSucceedUntilTheDirectoryIsSynced).
func TestCreateExclusive_DirectorySyncFailureLeavesTheCompletedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")

	t.Cleanup(fsync.SetForTest(func(string) error { return errors.New("sync refused") }))

	if err := CreateExclusive(path, []byte("payload"), 0o600); err == nil {
		t.Fatal("expected the directory-sync failure to propagate")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "payload" {
		t.Fatalf("content = %q err=%v, want the completed write \"payload\" left in place", got, err)
	}
}

// TestCreateExclusive_RefusesAnExistingDirectory verifies O_EXCL refuses a
// directory occupying path exactly as it refuses a plain file.
func TestCreateExclusive_RefusesAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("seed directory at path: %v", err)
	}

	var existsErr *FileExistsError
	if err := CreateExclusive(path, []byte("payload"), 0o600); !errors.As(err, &existsErr) {
		t.Fatalf("CreateExclusive over an existing directory = %v, want *FileExistsError", err)
	}
	fi, statErr := os.Lstat(path)
	if statErr != nil || !fi.IsDir() {
		t.Fatalf("Lstat(path) = %+v err=%v, want the untouched directory still there", fi, statErr)
	}
}

// TestWriteFileAtomic_CreateTempFailurePropagates verifies a CreateTemp
// failure (here: a parent directory that does not exist) is returned
// unwrapped rather than swallowed.
func TestWriteFileAtomic_CreateTempFailurePropagates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-subdir", "out")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected a CreateTemp failure over a missing directory")
	}
}

// TestCreateExclusive_OpenFailurePropagates verifies an OpenFile failure
// that is NOT "already exists" (here: a missing parent directory) is
// returned unwrapped rather than mapped to FileExistsError.
func TestCreateExclusive_OpenFailurePropagates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-subdir", "out")
	err := CreateExclusive(path, []byte("x"), 0o600)
	if err == nil {
		t.Fatal("expected an OpenFile failure over a missing directory")
	}
	var existsErr *FileExistsError
	if errors.As(err, &existsErr) {
		t.Fatalf("CreateExclusive over a missing directory = %v, want NOT *FileExistsError", err)
	}
}

// TestRefuseSymlink_LstatFailurePropagates verifies an Lstat failure that is
// NOT "does not exist" (here: a parent directory with its execute bit
// removed, which denies path resolution through it) is returned unwrapped.
func TestRefuseSymlink_LstatFailurePropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks, so this cannot be exercised as root")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // let t.TempDir() clean up

	path := filepath.Join(locked, "out")
	err := refuseSymlink(path)
	if err == nil {
		t.Fatal("expected an Lstat failure through a directory with no execute permission")
	}
	if os.IsNotExist(err) {
		t.Fatalf("refuseSymlink = %v, want a permission error, not IsNotExist", err)
	}
}

// Each of the four tests below forces one step of writeChmodSyncClose to
// fail, so every error-return path in WriteFileAtomic and CreateExclusive is
// exercised without depending on a real, hard-to-trigger OS fault (a full
// disk, a concurrent unlink). They restore the real implementation via
// t.Cleanup so no other test in the package observes the override.

func TestWriteFileAtomic_ChmodFailurePropagates(t *testing.T) {
	sentinel := errors.New("chmod refused")
	old := chmodFn
	chmodFn = func(*os.File, os.FileMode) error { return sentinel }
	t.Cleanup(func() { chmodFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("WriteFileAtomic = %v, want the chmod failure", err)
	}
}

func TestWriteFileAtomic_WriteFailurePropagates(t *testing.T) {
	sentinel := errors.New("write refused")
	old := writeFn
	writeFn = func(*os.File, []byte) error { return sentinel }
	t.Cleanup(func() { writeFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("WriteFileAtomic = %v, want the write failure", err)
	}
}

func TestWriteFileAtomic_SyncFailurePropagates(t *testing.T) {
	sentinel := errors.New("sync refused")
	old := syncFn
	syncFn = func(*os.File) error { return sentinel }
	t.Cleanup(func() { syncFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("WriteFileAtomic = %v, want the sync failure", err)
	}
}

func TestWriteFileAtomic_CloseFailurePropagates(t *testing.T) {
	sentinel := errors.New("close refused")
	old := closeFn
	closeFn = func(*os.File) error { return sentinel }
	t.Cleanup(func() { closeFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := WriteFileAtomic(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("WriteFileAtomic = %v, want the close failure", err)
	}
}

func TestCreateExclusive_ChmodFailureRemovesTheFile(t *testing.T) {
	sentinel := errors.New("chmod refused")
	old := chmodFn
	chmodFn = func(*os.File, os.FileMode) error { return sentinel }
	t.Cleanup(func() { chmodFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := CreateExclusive(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("CreateExclusive = %v, want the chmod failure", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("Lstat(path) after a chmod failure = %v, want IsNotExist", err)
	}
}

func TestCreateExclusive_WriteFailureRemovesTheFile(t *testing.T) {
	sentinel := errors.New("write refused")
	old := writeFn
	writeFn = func(*os.File, []byte) error { return sentinel }
	t.Cleanup(func() { writeFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := CreateExclusive(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("CreateExclusive = %v, want the write failure", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("Lstat(path) after a write failure = %v, want IsNotExist", err)
	}
}

func TestCreateExclusive_SyncFailureRemovesTheFile(t *testing.T) {
	sentinel := errors.New("sync refused")
	old := syncFn
	syncFn = func(*os.File) error { return sentinel }
	t.Cleanup(func() { syncFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := CreateExclusive(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("CreateExclusive = %v, want the sync failure", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("Lstat(path) after a sync failure = %v, want IsNotExist", err)
	}
}

func TestCreateExclusive_CloseFailureRemovesTheFile(t *testing.T) {
	sentinel := errors.New("close refused")
	old := closeFn
	closeFn = func(*os.File) error { return sentinel }
	t.Cleanup(func() { closeFn = old })

	path := filepath.Join(t.TempDir(), "out")
	if err := CreateExclusive(path, []byte("x"), 0o600); !errors.Is(err, sentinel) {
		t.Fatalf("CreateExclusive = %v, want the close failure", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("Lstat(path) after a close failure = %v, want IsNotExist", err)
	}
}

func TestSymlinkRefusedError_Error(t *testing.T) {
	err := &SymlinkRefusedError{Path: "/tmp/x"}
	if got := err.Error(); got == "" {
		t.Fatal("Error() returned an empty string")
	}
}

func TestFileExistsError_Error(t *testing.T) {
	err := &FileExistsError{Path: "/tmp/x"}
	if got := err.Error(); got == "" {
		t.Fatal("Error() returned an empty string")
	}
}
