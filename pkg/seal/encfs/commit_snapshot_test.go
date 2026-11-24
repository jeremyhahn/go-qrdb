package encfs

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// TestFramed_CommitAfterSizeRead commits one more frame through a live
// writer between a reader's file-size read and its commit-slot read, the
// window a concurrent WAL append+sync opens. The reader must see a
// consistent snapshot (the new frame included), never ErrAuthFailed.
func TestFramed_CommitAfterSizeRead(t *testing.T) {
	b := newFakeBarrier(t)
	rec := bytes.Repeat([]byte{'c'}, 100)
	for _, tc := range []struct {
		name string
		size func(fsys filesys.FS, path string) (int64, error)
	}{
		{"Open", func(fsys filesys.FS, path string) (int64, error) {
			f, err := fsys.Open(path)
			if err != nil {
				return 0, err
			}
			defer func() { _ = f.Close() }()
			fi, err := f.Stat()
			if err != nil {
				return 0, err
			}
			return fi.Size(), nil
		}},
		{"FS.Stat", func(fsys filesys.FS, path string) (int64, error) {
			fi, err := fsys.Stat(path)
			if err != nil {
				return 0, err
			}
			return fi.Size(), nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "000007.log")
			fsys := New(filesys.Default, b)
			w, err := fsys.Create(path)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			defer func() { _ = w.Close() }()
			if _, err := w.Write(rec); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := w.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			fired := false
			beforeCommitRead = func() {
				if fired {
					return
				}
				fired = true
				if _, err := w.Write(rec); err != nil {
					t.Errorf("hook Write: %v", err)
				}
				if err := w.Sync(); err != nil {
					t.Errorf("hook Sync: %v", err)
				}
			}
			t.Cleanup(func() { beforeCommitRead = nil })
			got, err := tc.size(fsys, path)
			if err != nil {
				t.Fatalf("%s with a commit between size and slot reads: %v", tc.name, err)
			}
			if !fired {
				t.Fatal("hook did not run")
			}
			if got != 200 {
				t.Fatalf("%s size = %d, want 200", tc.name, got)
			}
		})
	}
}
