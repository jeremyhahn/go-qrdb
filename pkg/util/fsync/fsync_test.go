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

// go-qrdb#268: a rename is a commit point only once the containing directory
// is durable. The post-state is identical either way, so every test here
// observes the OPERATION rather than the result.
package fsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// recorder replaces the directory-sync step, recording every directory it is
// asked to sync and returning err.
func recorder(t *testing.T, err error) *[]string {
	t.Helper()
	var seen []string
	restore := SetForTest(func(dir string) error {
		seen = append(seen, dir)
		return err
	})
	t.Cleanup(restore)
	return &seen
}

// write creates a file with content so a rename has something real to move.
func write(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
}

// TestRename_SyncsTheContainingDirectory: the whole point of the package. A
// same-parent rename syncs that one directory, exactly once.
func TestRename_SyncsTheContainingDirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "staged")
	dst := filepath.Join(dir, "live")
	write(t, src)

	seen := recorder(t, nil)
	if err := Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if len(*seen) != 1 || (*seen)[0] != dir {
		t.Errorf("synced dirs = %v; want exactly [%s]. A rename that reports success "+
			"before its directory entry is durable is not a commit point", *seen, dir)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("destination missing after rename: %v", err)
	}
}

// TestRename_DoesNotSucceedUntilTheDirectoryIsSynced is the mutation proof:
// the sync is failed at its boundary and the caller must see that failure.
// Removing the sync from Rename makes this test fail and no other.
func TestRename_DoesNotSucceedUntilTheDirectoryIsSynced(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "staged")
	write(t, src)

	sentinel := errors.New("directory sync refused")
	recorder(t, sentinel)

	err := Rename(src, filepath.Join(dir, "live"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Rename = %v; want the directory-sync failure. Reporting success before "+
			"the directory entry reaches stable storage is what lets a crash recover names "+
			"in an order they were never written in", err)
	}
}

// TestRename_AcrossDirectoriesSyncsBoth: a cross-parent rename changes two
// directories. Syncing only the destination leaves the source's removal of the
// old name undurable, so a crash can recover the entry under BOTH names.
func TestRename_AcrossDirectoriesSyncsBoth(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "from")
	to := filepath.Join(root, "to")
	for _, d := range []string{from, to} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	src := filepath.Join(from, "data")
	write(t, src)

	seen := recorder(t, nil)
	if err := Rename(src, filepath.Join(to, "data")); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got := map[string]int{}
	for _, d := range *seen {
		got[d]++
	}
	if got[from] != 1 || got[to] != 1 || len(*seen) != 2 {
		t.Errorf("synced dirs = %v; want %s and %s exactly once each. Syncing only the "+
			"destination leaves the old name's removal undurable, so a crash can recover "+
			"the entry under both names", *seen, from, to)
	}
}

// TestRename_FailedRenameIsReportedUnwrappedAndSkipsTheSync: callers keep
// os.IsNotExist handling, and there is nothing to make durable when nothing
// moved.
func TestRename_FailedRenameIsReportedUnwrappedAndSkipsTheSync(t *testing.T) {
	dir := t.TempDir()
	seen := recorder(t, nil)

	err := Rename(filepath.Join(dir, "absent"), filepath.Join(dir, "live"))
	if !os.IsNotExist(err) {
		t.Fatalf("Rename of a missing source = %v; want an IsNotExist error unwrapped, "+
			"so callers' existing handling still matches", err)
	}
	if len(*seen) != 0 {
		t.Errorf("synced %v after a failed rename; want none — nothing moved", *seen)
	}
}

// TestDir_PropagatesTheSyncOutcome covers both directions of the exported
// wrapper: it must return the failure rather than swallow it.
func TestDir_PropagatesTheSyncOutcome(t *testing.T) {
	dir := t.TempDir()

	sentinel := errors.New("directory sync refused")
	seen := recorder(t, sentinel)
	if err := Dir(dir); !errors.Is(err, sentinel) {
		t.Errorf("Dir = %v; want the sync failure propagated", err)
	}
	if len(*seen) != 1 || (*seen)[0] != dir {
		t.Errorf("synced dirs = %v; want [%s]", *seen, dir)
	}
}

// TestFsyncDir_RealFilesystem exercises the unseamed implementation, so the
// seam cannot be the only thing under test. A real directory syncs; a missing
// one fails at the open.
func TestFsyncDir_RealFilesystem(t *testing.T) {
	dir := t.TempDir()
	if err := fsyncDir(dir); err != nil {
		t.Errorf("fsyncDir(%s) = %v; want nil on a real directory", dir, err)
	}
	if err := fsyncDir(filepath.Join(dir, "absent")); !os.IsNotExist(err) {
		t.Errorf("fsyncDir on a missing directory = %v; want IsNotExist", err)
	}
}

// TestRename_RealFilesystemEndToEnd runs the whole operation with no seam
// installed, so a mistake in the unseamed path (a wrong directory, a panic on
// a bare filename) cannot hide behind the fault-injection tests.
func TestRename_RealFilesystemEndToEnd(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "staged")
	dst := filepath.Join(dir, "live")
	write(t, src)

	if err := Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "payload" {
		t.Fatalf("destination content = %q err=%v; want the source content", got, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present after rename: %v", err)
	}
}

// TestSetForTest_Restores: the seam is restore-only, and a test that leaked it
// would silently disable the durability of every later test in the binary.
func TestSetForTest_Restores(t *testing.T) {
	restore := SetForTest(func(string) error { return errors.New("installed") })
	if err := Dir(t.TempDir()); err == nil {
		t.Fatal("SetForTest did not install the replacement")
	}
	restore()
	if err := Dir(t.TempDir()); err != nil {
		t.Errorf("after restore, Dir = %v; want the real sync to be back", err)
	}
}
