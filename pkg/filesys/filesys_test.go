// Copyright (c) 2026 Jeremy Hahn
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

package filesys_test

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// writeFile creates name on the default FS with the given contents.
func writeFile(t *testing.T, name, contents string) {
	t.Helper()
	f, err := filesys.Default.Create(name)
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	if _, err := f.Write([]byte(contents)); err != nil {
		t.Fatalf("Write(%s): %v", name, err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync(%s): %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%s): %v", name, err)
	}
}

// readFile returns the contents of name on the default FS.
func readFile(t *testing.T, name string) string {
	t.Helper()
	f, err := filesys.Default.Open(name)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", name, err)
	}
	return string(b)
}

func TestUnsupportedError_Error(t *testing.T) {
	err := &filesys.UnsupportedError{Op: "lock"}
	if got, want := err.Error(), "filesys: lock not supported"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestUnsupportedError_IsErrUnsupported(t *testing.T) {
	var err error = &filesys.UnsupportedError{Op: "disk usage"}
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("errors.Is(%v, errors.ErrUnsupported) = false", err)
	}
	var ue *filesys.UnsupportedError
	if !errors.As(err, &ue) || ue.Op != "disk usage" {
		t.Errorf("errors.As did not recover the operation: %+v", ue)
	}
}

func TestDefault_CreateWritesReadableFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "hello")
	if got := readFile(t, name); got != "hello" {
		t.Errorf("contents = %q, want %q", got, "hello")
	}
}

func TestDefault_CreateReplacesExistingFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "the first, longer contents")
	writeFile(t, name, "second")
	if got := readFile(t, name); got != "second" {
		t.Errorf("contents = %q, want the replacement", got)
	}
}

func TestDefault_CreateInMissingDirFails(t *testing.T) {
	_, err := filesys.Default.Create(filepath.Join(t.TempDir(), "missing", "a"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Create in a missing dir: err = %v, want ErrNotExist", err)
	}
}

func TestDefault_OpenMissingFails(t *testing.T) {
	_, err := filesys.Default.Open(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open missing: err = %v, want ErrNotExist", err)
	}
}

func TestDefault_OpenIsReadOnly(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "x")
	f, err := filesys.Default.Open(name)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write([]byte("y")); err == nil {
		t.Error("Write on a file opened read-only succeeded")
	}
}

func TestDefault_OpenReadWriteCreatesMissingFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	f, err := filesys.Default.OpenReadWrite(name)
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	if _, err := f.WriteAt([]byte("abc"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readFile(t, name); got != "abc" {
		t.Errorf("contents = %q, want %q", got, "abc")
	}
}

func TestDefault_OpenReadWriteKeepsExistingContents(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "abc")
	f, err := filesys.Default.OpenReadWrite(name)
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	if _, err := f.WriteAt([]byte("Z"), 1); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	buf := make([]byte, 3)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "aZc" {
		t.Errorf("contents = %q, want %q", buf, "aZc")
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDefault_OpenDirSyncs(t *testing.T) {
	d, err := filesys.Default.OpenDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	if err := d.Sync(); err != nil {
		t.Errorf("Sync dir: %v", err)
	}
	fi, err := d.Stat()
	if err != nil || !fi.IsDir() {
		t.Errorf("Stat dir = %v, %v; want a directory", fi, err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close dir: %v", err)
	}
}

func TestDefault_Link(t *testing.T) {
	dir := t.TempDir()
	oldname, newname := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeFile(t, oldname, "linked")
	if err := filesys.Default.Link(oldname, newname); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got := readFile(t, newname); got != "linked" {
		t.Errorf("link contents = %q, want %q", got, "linked")
	}
	if err := filesys.Default.Link(oldname, newname); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Link onto an existing name: err = %v, want ErrExist", err)
	}
}

func TestDefault_Remove(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "x")
	if err := filesys.Default.Remove(name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := filesys.Default.Remove(name); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("second Remove: err = %v, want ErrNotExist", err)
	}
}

func TestDefault_RemoveAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := filesys.Default.MkdirAll(filepath.Join(dir, "e"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, filepath.Join(dir, "e", "a"), "x")
	if err := filesys.Default.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := filesys.Default.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat after RemoveAll: err = %v, want ErrNotExist", err)
	}
	if err := filesys.Default.RemoveAll(dir); err != nil {
		t.Errorf("RemoveAll of a missing dir: %v", err)
	}
}

func TestDefault_Rename(t *testing.T) {
	dir := t.TempDir()
	oldname, newname := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeFile(t, oldname, "moved")
	writeFile(t, newname, "replaced")
	if err := filesys.Default.Rename(oldname, newname); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := readFile(t, newname); got != "moved" {
		t.Errorf("contents = %q, want %q", got, "moved")
	}
	if err := filesys.Default.Rename(oldname, newname); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Rename of a missing file: err = %v, want ErrNotExist", err)
	}
}

func TestDefault_MkdirAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := filesys.Default.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := filesys.Default.MkdirAll(dir, 0o755); err != nil {
		t.Errorf("MkdirAll of an existing dir: %v", err)
	}
	fi, err := filesys.Default.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Errorf("Stat = %v, %v; want a directory", fi, err)
	}
}

func TestDefault_List(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a"), "x")
	writeFile(t, filepath.Join(dir, "b"), "y")
	names, err := filesys.Default.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"a", "b"}) {
		t.Errorf("List = %v, want [a b]", names)
	}
}

func TestDefault_ListMissingFails(t *testing.T) {
	if _, err := filesys.Default.List(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("List missing: err = %v, want ErrNotExist", err)
	}
}

func TestDefault_ListOfFileFails(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "x")
	if _, err := filesys.Default.List(name); err == nil {
		t.Error("List of a regular file succeeded")
	}
}

func TestDefault_Stat(t *testing.T) {
	name := filepath.Join(t.TempDir(), "a")
	writeFile(t, name, "four")
	fi, err := filesys.Default.Stat(name)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != 4 || fi.IsDir() {
		t.Errorf("Stat = size %d dir %v, want size 4 file", fi.Size(), fi.IsDir())
	}
	if _, err := filesys.Default.Stat(name + ".missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat missing: err = %v, want ErrNotExist", err)
	}
}

func TestDefault_IsOnHost(t *testing.T) {
	h, ok := filesys.Default.(filesys.HostFS)
	if !ok {
		t.Fatal("Default does not implement HostFS")
	}
	if !h.OnHost() {
		t.Error("Default.OnHost() = false, want true")
	}
}

// TestDefault_TakesNoLockOfItsOwn pins that Default keeps no lock
// bookkeeping of its own: a host file's lock belongs to the process, and a
// second record of what is held lets two stores in one process both take
// it (see HostFS).
func TestDefault_TakesNoLockOfItsOwn(t *testing.T) {
	if _, ok := filesys.Default.(filesys.LockFS); ok {
		t.Error("Default implements LockFS, want its locks taken by the one process-wide owner")
	}
}
