// Copyright 2026 Jeremy Hahn
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

package raftharness

import (
	"io/fs"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"
)

// StallFS is a waldb.FS over the real filesystem whose file Syncs park while
// a stall is armed: the shape of a WAL fsync stall on a contended disk. Every
// other operation passes through. Open a WAL over it with waldb.WithFS and
// hand the WAL to the host with quicraft.WithLogDB.
type StallFS struct {
	waldb.FS
	stall atomic.Pointer[Stall]
	syncs atomic.Uint64
}

var _ waldb.FS = (*StallFS)(nil)

// NewStallFS returns a StallFS over waldb.DefaultFS with no stall armed.
func NewStallFS() *StallFS {
	return &StallFS{FS: waldb.DefaultFS()}
}

// Stall is one armed fsync stall. Every Sync that starts while it is armed
// parks until Release, or until the bound given to Arm elapses.
type Stall struct {
	fs        *StallFS
	release   chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
	doneOnce  sync.Once
}

// Arm arms a stall that releases itself after bound, so a failing test can
// never park the WAL for longer. Arming while a stall is armed replaces it;
// the earlier stall's parked Syncs stay parked until it is released.
func (f *StallFS) Arm(bound time.Duration) *Stall {
	s := &Stall{fs: f, release: make(chan struct{}), entered: make(chan struct{})}
	f.stall.Store(s)
	// A bound that fires after Release finds the stall released: a no-op.
	time.AfterFunc(bound, s.Release)
	return s
}

// Syncs reports how many file Syncs have completed.
func (f *StallFS) Syncs() uint64 { return f.syncs.Load() }

// OpenFile opens name through the wrapped FS with a stallable Sync.
func (f *StallFS) OpenFile(name string, flag int, perm fs.FileMode) (waldb.File, error) {
	file, err := f.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &stallFile{File: file, fs: f}, nil
}

// Open opens name read-only through the wrapped FS with a stallable Sync.
func (f *StallFS) Open(name string) (waldb.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &stallFile{File: file, fs: f}, nil
}

// Entered is closed once the first Sync parks on this stall: the WAL write is
// in flight and its fsync has not returned.
func (s *Stall) Entered() <-chan struct{} { return s.entered }

// Release disarms the stall and lets every parked Sync complete. It is safe to
// call more than once.
func (s *Stall) Release() {
	s.doneOnce.Do(func() {
		s.fs.stall.CompareAndSwap(s, nil)
		close(s.release)
	})
}

func (s *Stall) park() {
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.release
}

type stallFile struct {
	waldb.File
	fs *StallFS
}

// Sync parks while a stall is armed, then syncs the file.
func (f *stallFile) Sync() error {
	if s := f.fs.stall.Load(); s != nil {
		s.park()
	}
	err := f.File.Sync()
	f.fs.syncs.Add(1)
	return err
}
