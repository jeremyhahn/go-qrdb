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

package file

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

// errFileFault is the failure faultFile injects.
var errFileFault = errors.New("injected file fault")

// faultFS creates real files through the wrapped FS and hands them back
// wrapped in a faultFile that fails the one operation named by failOn.
type faultFS struct {
	filesys.FS
	failOn string
}

// Create creates the file and wraps it so failOn fails.
func (fs faultFS) Create(name string) (filesys.File, error) {
	f, err := fs.FS.Create(name)
	if err != nil {
		return nil, err
	}
	return faultFile{File: f, failOn: fs.failOn}, nil
}

// faultFile fails Write, Sync or Close when failOn names it. A failed Close
// still closes the underlying file so the test leaks no descriptor.
type faultFile struct {
	filesys.File
	failOn string
}

// Write fails when failOn is "write".
func (f faultFile) Write(p []byte) (int, error) {
	if f.failOn == "write" {
		return 0, errFileFault
	}
	return f.File.Write(p)
}

// Sync fails when failOn is "sync".
func (f faultFile) Sync() error {
	if f.failOn == "sync" {
		return errFileFault
	}
	return f.File.Sync()
}

// Close fails when failOn is "close".
func (f faultFile) Close() error {
	err := f.File.Close()
	if f.failOn == "close" {
		return errFileFault
	}
	return err
}

// tempFilesIn returns the names in dir that carry prefix.
func tempFilesIn(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), prefix) {
			names = append(names, ent.Name())
		}
	}
	return names
}

// TestWriteDurable_FileFaultsPropagateAndDiscardTemp covers every failure
// writeDurable can meet on the temp file itself: the error reaches Set's
// caller, the record keeps its previous value, and no temp file is left
// beside it.
func TestWriteDurable_FileFaultsPropagateAndDiscardTemp(t *testing.T) {
	for _, failOn := range []string{"write", "sync", "close"} {
		t.Run(failOn, func(t *testing.T) {
			e, err := New(memdir.TempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close() }()

			key := []byte("k")
			if err := e.Set(key, []byte("v1")); err != nil {
				t.Fatalf("seed Set: %v", err)
			}
			e.fs = faultFS{FS: e.fs, failOn: failOn}

			if err := e.Set(key, []byte("v2")); !errors.Is(err, errFileFault) {
				t.Fatalf("Set = %v, want the injected %s fault", err, failOn)
			}
			got, err := e.Get(key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(got) != "v1" {
				t.Errorf("Get = %q, want the previous value %q", got, "v1")
			}
			shardDir, _ := mustRecordPaths(t, e, key)
			if left := tempFilesIn(t, shardDir, tmpSetPrefix); len(left) != 0 {
				t.Errorf("temp files left after a failed %s: %v", failOn, left)
			}
		})
	}
}

// TestWriteDurable_RenameFailureDiscardsTemp covers the rename step: a
// target that is a non-empty directory cannot be replaced by a file, so the
// rename fails, the error is returned, and the synced temp file is removed.
func TestWriteDurable_RenameFailureDiscardsTemp(t *testing.T) {
	e, err := New(memdir.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()

	target := filepath.Join(e.dir, "occupied")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	err = e.writeDurable(e.fs, target, tmpSetPrefix, []byte("data"))
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("writeDurable = %v, want *os.LinkError from the rename", err)
	}
	if left := tempFilesIn(t, e.dir, tmpSetPrefix); len(left) != 0 {
		t.Errorf("temp files left after a failed rename: %v", left)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Errorf("target = %v, %v; want the directory left in place", info, err)
	}
}

// TestApplyBufferedOps_ErrorPropagates covers applyBufferedOps' two error
// returns around a range op: the point ops before it, and the range delete
// itself.
func TestApplyBufferedOps_ErrorPropagates(t *testing.T) {
	cases := map[string]struct {
		setup func(t *testing.T, e *Engine)
		ops   []storage.BufferedOp
		want  error
	}{
		"point-op-before-range": {
			want: storage.ErrEmptyKey,
			ops: []storage.BufferedOp{
				{Key: []byte{}, Value: []byte("v")},
				{Key: []byte("a"), RangeEnd: []byte("z"), Range: true},
			},
		},
		"range-delete": {
			want: syscall.EISDIR,
			setup: func(t *testing.T, e *Engine) {
				if err := e.Set([]byte("m"), []byte("v")); err != nil {
					t.Fatal(err)
				}
				replaceRecordWithDirLink(t, e, []byte("m"))
			},
			ops: []storage.BufferedOp{
				{Key: []byte("b"), Value: []byte("v")},
				{Key: []byte("a"), RangeEnd: []byte("z"), Range: true},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, err := New(memdir.TempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Close() }()
			if tc.setup != nil {
				tc.setup(t, e)
			}
			if err := e.applyBufferedOps(tc.ops); !errors.Is(err, tc.want) {
				t.Fatalf("applyBufferedOps = %v, want %v", err, tc.want)
			}
		})
	}
}
