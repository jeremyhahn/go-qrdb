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

package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// heldWALFS is a file system whose barrier has a quiesce gate and whose
// next WAL sync, once armed, blocks until the test releases it: a
// state-machine commit then sits inside its quiesce lease on demand.
type heldWALFS struct {
	vfs.FS
	gate    *quiesce.Gate
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (fs *heldWALFS) Quiesce() *quiesce.Gate { return fs.gate }

func (fs *heldWALFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	f, err := fs.FS.Create(name, category)
	if err != nil || !strings.HasSuffix(name, ".log") {
		return f, err
	}
	return &heldWALFile{File: f, fs: fs}, nil
}

// heldWALFile blocks the armed sync of a WAL file.
type heldWALFile struct {
	vfs.File
	fs *heldWALFS
}

func (f *heldWALFile) hold() {
	if f.fs.armed.CompareAndSwap(true, false) {
		f.fs.entered <- struct{}{}
		<-f.fs.release
	}
}

func (f *heldWALFile) Sync() error {
	f.hold()
	return f.File.Sync()
}

func (f *heldWALFile) SyncData() error {
	f.hold()
	return f.File.SyncData()
}

func (f *heldWALFile) SyncTo(length int64) (bool, error) {
	f.hold()
	return f.File.SyncTo(length)
}

type committer interface {
	Open(context.Context, string, <-chan struct{}) (uint64, error)
	Update(context.Context, []sm.Entry, []sm.Result) error
	Close(context.Context) error
}

// TestStateMachineCommit_HoldsTheSealDrain proves a seal's drain waits for
// a state-machine commit on both families: it reports the store held while
// the commit is in flight, times out naming it, and drains once it returns.
func TestStateMachineCommit_HoldsTheSealDrain(t *testing.T) {
	cases := []struct {
		name string
		open func(*pebble.Options) committer
	}{
		{"mvcc", func(o *pebble.Options) committer { return NewMVCCStateMachine(o) }},
		{"pebble", func(o *pebble.Options) committer { return NewPebbleStateMachine(o) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gate quiesce.Gate
			fs := &heldWALFS{
				FS:      vfs.NewMem(),
				gate:    &gate,
				entered: make(chan struct{}),
				release: make(chan struct{}),
			}
			s := tc.open(&pebble.Options{FS: fs})
			if _, err := s.Open(context.Background(), "sm", nil); err != nil {
				t.Fatalf("Open: %v", err)
			}
			cmd, err := protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0)
			if err != nil {
				t.Fatal(err)
			}

			fs.armed.Store(true)
			updated := make(chan error, 1)
			go func() {
				updated <- s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, make([]sm.Result, 1))
			}()
			select {
			case <-fs.entered:
			case err := <-updated:
				t.Fatalf("Update returned %v without a synced WAL write", err)
			}

			gate.Close()
			var timeout *quiesce.DrainTimeoutError
			if err := gate.Drain(time.Millisecond); !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{"sm"}) {
				t.Fatalf("Drain with the commit held = %v; want a DrainTimeoutError naming sm", err)
			}

			drained := make(chan error, 1)
			go func() { drained <- gate.Drain(time.Minute) }()
			close(fs.release)
			if err := <-updated; err != nil {
				t.Fatalf("Update: %v", err)
			}
			if err := <-drained; err != nil {
				t.Fatalf("Drain after the commit returned = %v", err)
			}
			gate.Reopen()
			if err := s.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}

// TestStateMachineCommit_RefusedWhileSealing proves a state-machine commit
// that meets a closed gate is refused with the typed sealed error.
func TestStateMachineCommit_RefusedWhileSealing(t *testing.T) {
	var gate quiesce.Gate
	fs := &heldWALFS{FS: vfs.NewMem(), gate: &gate}
	s := NewPebbleStateMachine(&pebble.Options{FS: fs})
	if _, err := s.Open(context.Background(), "sm", nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	cmd, err := protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0)
	if err != nil {
		t.Fatal(err)
	}
	gate.Close()
	defer gate.Reopen()
	var sealed *pebbleStorage.SealedError
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, make([]sm.Result, 1)); !errors.As(err, &sealed) {
		t.Fatalf("Update while sealing = %v; want a *SealedError", err)
	}
}
