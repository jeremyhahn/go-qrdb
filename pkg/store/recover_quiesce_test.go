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
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

type recoverer interface {
	committer
	PrepareSnapshot() (interface{}, error)
	SaveSnapshot(context.Context, interface{}, io.Writer, <-chan struct{}) error
	RecoverFromSnapshot(context.Context, io.Reader, <-chan struct{}) error
}

var recoverFamilies = []struct {
	name string
	open func(*pebble.Options) recoverer
}{
	{"mvcc", func(o *pebble.Options) recoverer { return NewMVCCStateMachine(o) }},
	{"pebble", func(o *pebble.Options) recoverer { return NewPebbleStateMachine(o) }},
}

// openRecoverer opens a state machine on o and applies a put of each key.
func openRecoverer(t *testing.T, open func(*pebble.Options) recoverer, o *pebble.Options, keys ...string) recoverer {
	t.Helper()
	s := open(o)
	if _, err := s.Open(context.Background(), "sm", nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	for i, k := range keys {
		cmd, err := protocol.EncodeMVCCPut([]byte(k), []byte("v-"+k), 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Update(context.Background(), []sm.Entry{{Index: uint64(i + 1), Cmd: cmd}}, make([]sm.Result, 1)); err != nil {
			t.Fatalf("Update %s: %v", k, err)
		}
	}
	return s
}

// snapshotOf returns s's snapshot stream.
func snapshotOf(t *testing.T, s recoverer) []byte {
	t.Helper()
	snap, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snap, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	return buf.Bytes()
}

// TestRecoverFromSnapshot_HoldsTheSealDrain proves, on both families, that
// a seal arriving between recovery's delete of the old data and the commit
// of its replacement drains until recovery has finished, and that the
// recovered data is then complete.
func TestRecoverFromSnapshot_HoldsTheSealDrain(t *testing.T) {
	for _, tc := range recoverFamilies {
		t.Run(tc.name, func(t *testing.T) {
			src := openRecoverer(t, tc.open, &pebble.Options{FS: vfs.NewMem()}, "a", "b", "c")
			want := snapshotOf(t, src)

			var gate quiesce.Gate
			dst := openRecoverer(t, tc.open, &pebble.Options{FS: &heldWALFS{FS: vfs.NewMem(), gate: &gate}}, "old")

			cleared := make(chan struct{})
			resume := make(chan struct{})
			recoverClearedHook = func() {
				cleared <- struct{}{}
				<-resume
			}
			defer func() { recoverClearedHook = nil }()

			recovered := make(chan error, 1)
			go func() {
				recovered <- dst.RecoverFromSnapshot(context.Background(), bytes.NewReader(want), nil)
			}()
			select {
			case <-cleared:
			case err := <-recovered:
				t.Fatalf("RecoverFromSnapshot returned %v without clearing the old data", err)
			}

			gate.Close()
			var timeout *quiesce.DrainTimeoutError
			if err := gate.Drain(time.Millisecond); !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{"sm"}) {
				t.Fatalf("Drain mid-recovery = %v; want a DrainTimeoutError naming sm", err)
			}

			drained := make(chan error, 1)
			go func() { drained <- gate.Drain(time.Minute) }()
			close(resume)
			if err := <-recovered; err != nil {
				t.Fatalf("RecoverFromSnapshot: %v", err)
			}
			if err := <-drained; err != nil {
				t.Fatalf("Drain after recovery returned = %v", err)
			}
			gate.Reopen()

			if got := snapshotOf(t, dst); !bytes.Equal(got, want) {
				t.Fatalf("recovered snapshot differs from its source: %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

// TestRecoverFromSnapshot_RefusedWhileSealing proves, on both families,
// that recovery meeting a closed gate is refused with the typed sealed
// error before it deletes anything.
func TestRecoverFromSnapshot_RefusedWhileSealing(t *testing.T) {
	for _, tc := range recoverFamilies {
		t.Run(tc.name, func(t *testing.T) {
			src := openRecoverer(t, tc.open, &pebble.Options{FS: vfs.NewMem()}, "a", "b", "c")
			snap := snapshotOf(t, src)

			var gate quiesce.Gate
			dst := openRecoverer(t, tc.open, &pebble.Options{FS: &heldWALFS{FS: vfs.NewMem(), gate: &gate}}, "old")
			before := snapshotOf(t, dst)

			gate.Close()
			var sealed *pebbleStorage.SealedError
			if err := dst.RecoverFromSnapshot(context.Background(), bytes.NewReader(snap), nil); !errors.As(err, &sealed) {
				t.Fatalf("RecoverFromSnapshot while sealing = %v; want a *SealedError", err)
			}
			gate.Reopen()

			if got := snapshotOf(t, dst); !bytes.Equal(got, before) {
				t.Fatal("a refused recovery changed the old data")
			}
		})
	}
}
