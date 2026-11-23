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

package remotelsm

import (
	"errors"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/cockroachdb/pebble/v2/vfs"
)

var errInjected = errors.New("injected fault")

// faultFS wraps a memory FS and fails the operation named by failOp.
type faultFS struct {
	vfs.FS
	failOp string
}

func (f *faultFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	if f.failOp == "open" {
		return nil, errInjected
	}
	file, err := f.FS.Open(name, opts...)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, failOp: f.failOp}, nil
}

func (f *faultFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if f.failOp == "create" {
		return nil, errInjected
	}
	file, err := f.FS.Create(name, category)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, failOp: f.failOp}, nil
}

func (f *faultFS) Rename(oldname, newname string) error {
	if f.failOp == "rename" {
		return errInjected
	}
	return f.FS.Rename(oldname, newname)
}

func (f *faultFS) OpenDir(name string) (vfs.File, error) {
	if f.failOp == "opendir" {
		return nil, errInjected
	}
	d, err := f.FS.OpenDir(name)
	if err != nil {
		return nil, err
	}
	op := f.failOp
	if op == "dirsync" {
		op = "sync"
	}
	return &faultFile{File: d, failOp: op}, nil
}

// faultFile fails Read, Write, Sync or Close as its failOp names.
type faultFile struct {
	vfs.File
	failOp string
}

func (f *faultFile) Read(p []byte) (int, error) {
	if f.failOp == "read" {
		return 0, errInjected
	}
	return f.File.Read(p)
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.failOp == "write" {
		return 0, errInjected
	}
	return f.File.Write(p)
}

func (f *faultFile) Sync() error {
	if f.failOp == "sync" {
		return errInjected
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	err := f.File.Close()
	if f.failOp == "close" {
		return errInjected
	}
	return err
}

func newMemDir(t *testing.T) (vfs.FS, string) {
	t.Helper()
	mem := vfs.NewMem()
	if err := mem.MkdirAll("db", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return mem, "db"
}

func TestPanicMessage(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{errors.New("from error"), "from error"},
		{"from string", "from string"},
		{42, "42"},
	}
	for _, c := range cases {
		if got := panicMessage(c.in); got != c.want {
			t.Errorf("panicMessage(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCreatorIDError_Error(t *testing.T) {
	err := &CreatorIDError{Path: "db/" + creatorIDFile, Reason: "zero creator id"}
	want := "pebble creator id: db/" + creatorIDFile + ": zero creator id"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestLoadCreatorID_GeneratesThenReloads(t *testing.T) {
	mem, dir := newMemDir(t)
	id, err := loadCreatorID(mem, dir)
	if err != nil || id == 0 {
		t.Fatalf("first load: id=%d err=%v", id, err)
	}
	again, err := loadCreatorID(mem, dir)
	if err != nil || again != id {
		t.Fatalf("reload: id=%d err=%v, want %d", again, err, id)
	}
	if _, err := mem.Stat(mem.PathJoin(dir, creatorIDFile+".tmp")); err == nil {
		t.Fatal("temporary marker file left behind")
	}
}

func TestLoadCreatorID_OpenError(t *testing.T) {
	mem, dir := newMemDir(t)
	_, err := loadCreatorID(&faultFS{FS: mem, failOp: "open"}, dir)
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected open fault", err)
	}
}

func TestLoadCreatorID_ReadError(t *testing.T) {
	mem, dir := newMemDir(t)
	if _, err := loadCreatorID(mem, dir); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	_, err := loadCreatorID(&faultFS{FS: mem, failOp: "read"}, dir)
	var cerr *CreatorIDError
	if !errors.As(err, &cerr) || !strings.Contains(cerr.Reason, errInjected.Error()) {
		t.Fatalf("err = %v, want a CreatorIDError carrying the read fault", err)
	}
}

func TestLoadCreatorID_WriteError(t *testing.T) {
	mem, dir := newMemDir(t)
	_, err := loadCreatorID(&faultFS{FS: mem, failOp: "create"}, dir)
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected create fault", err)
	}
	if _, err := mem.Stat(mem.PathJoin(dir, creatorIDFile)); err == nil {
		t.Fatal("marker file written despite the fault")
	}
}

func TestWriteCreatorID_Faults(t *testing.T) {
	for _, op := range []string{"create", "write", "sync", "close", "rename", "opendir", "dirsync"} {
		t.Run(op, func(t *testing.T) {
			mem, dir := newMemDir(t)
			path := mem.PathJoin(dir, creatorIDFile)
			err := writeCreatorID(&faultFS{FS: mem, failOp: op}, dir, path, 7)
			if !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected %s fault", err, op)
			}
		})
	}
}

func TestWriteCreatorID_RoundTrip(t *testing.T) {
	mem, dir := newMemDir(t)
	path := mem.PathJoin(dir, creatorIDFile)
	if err := writeCreatorID(mem, dir, path, 12345); err != nil {
		t.Fatalf("writeCreatorID: %v", err)
	}
	got, err := loadCreatorID(mem, dir)
	if err != nil || got != 12345 {
		t.Fatalf("loadCreatorID = %d, %v; want 12345", got, err)
	}
}

// openSharedDB opens a pebble DB on a memory FS with in-memory shared
// storage, so SetCreatorID takes effect.
func openSharedDB(t *testing.T) *pebble.DB {
	t.Helper()
	opts := &pebble.Options{FS: vfs.NewMem()}
	opts.Experimental.RemoteStorage = remote.MakeSimpleFactory(map[remote.Locator]remote.Storage{
		"": remote.NewInMem(),
	})
	opts.Experimental.CreateOnShared = remote.CreateOnSharedAll
	db, err := pebble.Open("db", opts)
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	return db
}

func TestSetCreatorID_SameIDThenChanged(t *testing.T) {
	db := openSharedDB(t)
	defer func() { _ = db.Close() }()
	if err := setCreatorID(db, "marker", 5); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := setCreatorID(db, "marker", 5); err != nil {
		t.Fatalf("same id again: %v", err)
	}
	err := setCreatorID(db, "marker", 6)
	var cerr *CreatorIDError
	if !errors.As(err, &cerr) || cerr.Path != "marker" {
		t.Fatalf("err = %v, want a CreatorIDError for marker", err)
	}
}

func TestSetCreatorID_ForeignPanicPropagates(t *testing.T) {
	db := openSharedDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	defer func() {
		r := recover()
		if r == nil {
			return // SetCreatorID on a closed DB did not panic: nothing to propagate.
		}
		if strings.HasPrefix(panicMessage(r), creatorIDPanicPrefix) {
			t.Fatalf("closed-DB panic %v mistaken for a creator id change", r)
		}
	}()
	_ = setCreatorID(db, "marker", 9)
}
