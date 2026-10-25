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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// epochSealedFS refuses to create an sstable while sealed, with go-quicraft's
// sentinel: the state a closing quicraft Host leaves an encrypting filesystem
// in when it seals the EpochBarrier under a still-unsealed StorageBarrier.
type epochSealedFS struct {
	vfs.FS
	sealed atomic.Bool
}

func (fs *epochSealedFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if fs.sealed.Load() && strings.HasSuffix(name, ".sst") {
		return nil, seal.ErrBarrierSealed
	}
	return fs.FS.Create(name, category)
}

// syncer is the part of both disk state machines a snapshot drives.
type syncer interface {
	Open(ctx context.Context, dir string, stopc <-chan struct{}) (uint64, error)
	Sync() error
	Close(ctx context.Context) error
}

func TestStateMachineSync_SealedBarrierEndsWithTypedError(t *testing.T) {
	cases := []struct {
		name string
		open func(*pebble.Options) (syncer, func() *pebble.DB)
	}{
		{"mvcc", func(o *pebble.Options) (syncer, func() *pebble.DB) {
			s := NewMVCCStateMachine(o)
			return s, func() *pebble.DB { return s.db }
		}},
		{"pebble", func(o *pebble.Options) (syncer, func() *pebble.DB) {
			s := NewPebbleStateMachine(o)
			return s, func() *pebble.DB { return s.db }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &epochSealedFS{FS: vfs.NewMem()}
			sm, db := tc.open(&pebble.Options{FS: fs})
			if _, err := sm.Open(context.Background(), "sm", nil); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := db().Set([]byte("k"), []byte("v"), pebble.NoSync); err != nil {
				t.Fatalf("Set: %v", err)
			}
			fs.sealed.Store(true)

			synced := make(chan error, 1)
			go func() { synced <- sm.Sync() }()
			var err error
			select {
			case err = <-synced:
			case <-time.After(30 * time.Second):
				t.Fatal("Sync is still waiting on a flush the sealed barrier refuses")
			}
			var write *WriteError
			var sealed *pebbleStorage.SealedError
			if !errors.As(err, &write) || !errors.As(err, &sealed) || !barrierSealed(err) {
				t.Fatalf("Sync = %v; want a *WriteError wrapping a *SealedError", err)
			}

			// The writer stops: Close returns with the barrier still sealed.
			if err := sm.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}

func TestStateMachineSync_UnsealedBarrierFlushes(t *testing.T) {
	s := NewMVCCStateMachine(&pebble.Options{FS: vfs.NewMem()})
	if _, err := s.Open(context.Background(), "sm", nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err := s.db.Set([]byte("k"), []byte("v"), pebble.NoSync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync = %v; want nil", err)
	}
	if err := s.seals.Err(); err != nil {
		t.Fatalf("seal watch Err() = %v; want nil", err)
	}
}

func TestBarrierSealed(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{seal.ErrSealed, true},
		{seal.ErrBarrierSealed, true},
		{&WriteError{Err: seal.ErrBarrierSealed}, true},
		{errors.New("disk full"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := barrierSealed(tc.err); got != tc.want {
			t.Errorf("barrierSealed(%v) = %v; want %v", tc.err, got, tc.want)
		}
	}
}

func TestOpenPebble_DoesNotModifyCallerOptions(t *testing.T) {
	opts := &pebble.Options{FS: vfs.NewMem()}
	db, seals, err := openPebble("sm", opts)
	if err != nil {
		t.Fatalf("openPebble: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if seals == nil {
		t.Fatal("openPebble returned no seal watch")
	}
	if opts.EventListener != nil || opts.Logger != nil {
		t.Fatal("openPebble attached its watch to the caller's options")
	}
	if _, _, err := openPebble("sm", opts); err == nil {
		t.Fatal("a second open of a locked directory succeeded")
	}
}
