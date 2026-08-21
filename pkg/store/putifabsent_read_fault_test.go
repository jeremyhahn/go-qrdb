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

package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// pebbleHandlePutIfAbsent decides absence by classifying getKeyInBatch's
// error: *kv.KeyNotFoundError means the key is not there, anything else is a
// read fault it must not decide on. That classification is a COUPLING — if
// getKeyInBatch ever reported absence as some other type, the handler would
// start treating every missing key as a fault and no conditional put would
// ever store; report a fault as absence, and it would overwrite the holder
// and diverge replicas that read successfully.
//
// This pins both halves of the coupling the handler depends on.
func TestPebble_getKeyInBatch_DistinguishesAbsenceFromFault(t *testing.T) {
	s := newTestPebbleSM(t)

	batch := s.db.NewIndexedBatch()
	defer func() { _ = batch.Close() }()

	// A missing key is reported as absence, specifically typed.
	_, err := s.getKeyInBatch(batch, []byte("never-written"))
	if err == nil {
		t.Fatal("getKeyInBatch found a key that was never written")
	}
	var notFound *kv.KeyNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("absence reported as %T (%v), want *kv.KeyNotFoundError — the "+
			"conditional-put handler classifies on exactly this type", err, err)
	}

	// A present key is readable, so the handler declines rather than storing.
	if serr := batch.Set([]byte("present"), []byte("v"), nil); serr != nil {
		t.Fatalf("Set: %v", serr)
	}
	if _, gerr := s.getKeyInBatch(batch, []byte("present")); gerr != nil {
		t.Fatalf("getKeyInBatch(present) = %v, want nil", gerr)
	}
}

// The conditional put declines an existing key and stores an absent one,
// through the state machine's real apply path.
func TestPebbleStateMachine_PutIfAbsent_DeclinesWhenPresent(t *testing.T) {
	s := newTestPebbleSM(t)
	ctx := context.Background()

	entries := []sm.Entry{
		baseKVPutIfAbsentEntry(t, 1, []byte("claim"), []byte("acme")),
		baseKVPutIfAbsentEntry(t, 2, []byte("claim"), []byte("globex")),
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(ctx, entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if results[0].Value != kv.ResultStored {
		t.Fatalf("first = %d, want kv.ResultStored", results[0].Value)
	}
	if results[1].Value != kv.ResultNotStored {
		t.Fatalf("second = %d, want kv.ResultNotStored — the loser overwrote the "+
			"winner", results[1].Value)
	}
}

// newReadFaultPebbleSM opens a PebbleStateMachine whose file reads can be made
// to fail on demand. Arming only AFTER a flush is what makes the fault
// reachable: an unflushed key is served from the memtable and never touches
// the filesystem.
func newReadFaultPebbleSM(t *testing.T) (*PebbleStateMachine, *atomic.Bool) {
	t.Helper()
	var armed atomic.Bool
	fs := errorfs.Wrap(vfs.NewMem(), errorfs.InjectorFunc(func(op errorfs.Op) error {
		if armed.Load() && (op.Kind == errorfs.OpFileRead ||
			op.Kind == errorfs.OpFileReadAt || op.Kind == errorfs.OpOpen) {
			return errorfs.ErrInjected
		}
		return nil
	}))
	s := NewPebbleStateMachine(&pebble.Options{FS: fs, Cache: pebble.NewCache(1)})
	ctx := context.Background()
	if _, err := s.Open(ctx, t.TempDir(), make(chan struct{})); err != nil {
		t.Fatalf("open pebble sm: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	return s, &armed
}

// A conditional put whose EXISTENCE READ fails must not decide. Reading a
// fault as absence overwrites the holder and reports ResultStored — and
// because the fault is LOCAL to one replica, replicas whose reads succeeded
// decline the same entry this one stored and the group diverges from an
// identical log.
//
// Both opcodes are covered: the base-KV handler was corrected first and the
// MVCC one kept reading every error as absence, so a table here is what stops
// the two drifting apart again.
func TestPebbleStateMachine_PutIfAbsent_AFailedExistenceReadDoesNotStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry func(idx uint64, key, value []byte) sm.Entry
	}{
		{
			name: "base KV opcode",
			entry: func(idx uint64, key, value []byte) sm.Entry {
				return baseKVPutIfAbsentEntry(t, idx, key, value)
			},
		},
		{
			name: "MVCC opcode",
			entry: func(idx uint64, key, value []byte) sm.Entry {
				return sm.Entry{Index: idx, Cmd: protocol.EncodeMVCCPutIfAbsent(key, value, 0)}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, armed := newReadFaultPebbleSM(t)
			ctx := context.Background()
			key := []byte("claim")

			// Seed the holder, then flush so the key is read from a file.
			results := make([]sm.Result, 1)
			if err := s.Update(ctx, []sm.Entry{tc.entry(1, key, []byte("acme"))}, results); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := s.db.Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			armed.Store(true)
			err := s.Update(ctx, []sm.Entry{tc.entry(2, key, []byte("globex"))}, make([]sm.Result, 1))
			armed.Store(false)

			if !errors.Is(err, errorfs.ErrInjected) {
				t.Fatalf("Update = %v, want the read fault propagated — a fault read "+
					"as absence overwrites the holder and diverges replicas", err)
			}

			// The holder survives: nothing was decided or written.
			got, closer, gerr := s.db.Get(key)
			if gerr != nil {
				t.Fatalf("Get after the failed read: %v", gerr)
			}
			value := string(got)
			_ = closer.Close()
			if value != "acme" {
				t.Fatalf("value after a failed existence read = %q, want the holder's "+
					"%q", value, "acme")
			}
		})
	}
}

// The guarded delete on the Pebble state machine, held to the same contract
// as KVEngine's and MVCCEngine's: delete on a match, refuse on a mismatch
// without touching the holder's value, and refuse a read fault rather than
// deciding on it.
func TestPebbleStateMachine_CompareAndDelete_Contract(t *testing.T) {
	entry := func(idx uint64, key, expected string) sm.Entry {
		cmd, err := protocol.EncodeCompareAndDelete([]byte(key), []byte(expected))
		if err != nil {
			t.Fatalf("EncodeCompareAndDelete: %v", err)
		}
		return sm.Entry{Index: idx, Cmd: cmd}
	}

	t.Run("refuses on a mismatch and keeps the holder", func(t *testing.T) {
		s := newTestPebbleSM(t)
		ctx := context.Background()

		results := make([]sm.Result, 2)
		if err := s.Update(ctx, []sm.Entry{
			baseKVPutIfAbsentEntry(t, 1, []byte("claim"), []byte("acme")),
			entry(2, "claim", "globex"),
		}, results); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if results[1].Value != protocol.ResultNotDeleted {
			t.Fatalf("Value = %d, want ResultNotDeleted", results[1].Value)
		}
		got, closer, gerr := s.db.Get([]byte("claim"))
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		value := string(got)
		_ = closer.Close()
		if value != "acme" {
			t.Fatalf("value = %q, want the holder's %q", value, "acme")
		}
	})

	t.Run("deletes on a match", func(t *testing.T) {
		s := newTestPebbleSM(t)
		ctx := context.Background()

		results := make([]sm.Result, 2)
		if err := s.Update(ctx, []sm.Entry{
			baseKVPutIfAbsentEntry(t, 1, []byte("claim"), []byte("acme")),
			entry(2, "claim", "acme"),
		}, results); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if results[1].Value != protocol.ResultDeleted {
			t.Fatalf("Value = %d, want ResultDeleted", results[1].Value)
		}
		if _, closer, gerr := s.db.Get([]byte("claim")); gerr == nil {
			_ = closer.Close()
			t.Fatal("the key survived a matching guarded delete")
		}
	})

	t.Run("a failed guard read deletes nothing", func(t *testing.T) {
		s, armed := newReadFaultPebbleSM(t)
		ctx := context.Background()

		if err := s.Update(ctx, []sm.Entry{
			baseKVPutIfAbsentEntry(t, 1, []byte("claim"), []byte("acme")),
		}, make([]sm.Result, 1)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := s.db.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}

		armed.Store(true)
		err := s.Update(ctx, []sm.Entry{entry(2, "claim", "acme")}, make([]sm.Result, 1))
		armed.Store(false)

		if !errors.Is(err, errorfs.ErrInjected) {
			t.Fatalf("Update = %v, want the read fault propagated", err)
		}
		got, closer, gerr := s.db.Get([]byte("claim"))
		if gerr != nil {
			t.Fatalf("Get after the failed guard read: %v", gerr)
		}
		value := string(got)
		_ = closer.Close()
		if value != "acme" {
			t.Fatalf("value = %q, want %q", value, "acme")
		}
	})
}
