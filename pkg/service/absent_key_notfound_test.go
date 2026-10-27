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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	memorystorage "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// naLookupHost is the read half of the raft host as an sm.NALookup state
// machine actually reaches it (go-qrdb#214).
//
// The point-lookup path is reproduced exactly rather than approximated: the
// value comes from a REAL statemachine.DiskBackend over a real storage engine
// — the same call DiskAdapter.BytesLookup makes — and it is then returned
// straight out of an interface{}-typed method, which is what go-quicraft's
// rsm.StateMachine.Lookup fast path does and is where the typed nil gets
// boxed into a non-nil interface (go-quicraft#73). Faking the boxing by
// hand-writing []byte(nil) into the mock would let the test pass while the
// adapters had stopped producing it.
//
// boxLookup is the mutation switch: setting it false makes the fast path
// return an untyped nil, which is the shape the generic Lookup path returns
// and the shape this repo's service layer used to assume universally.
type naLookupHost struct {
	mockHost
	backend   *statemachine.DiskBackend
	boxLookup bool
}

var _ AdminHost = (*naLookupHost)(nil)

func (h *naLookupHost) lookup(query interface{}) (interface{}, error) {
	key, ok := query.([]byte)
	if !ok {
		return nil, nil
	}
	val, err := h.backend.Get(key)
	if err != nil {
		return nil, err
	}
	if !h.boxLookup && val == nil {
		return nil, nil
	}
	return val, nil
}

func (h *naLookupHost) SyncRead(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
	return h.lookup(query)
}

func (h *naLookupHost) StaleRead(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
	return h.lookup(query)
}

func (h *naLookupHost) ReadWithConsistency(_ context.Context, _ uint64, query interface{}, _ consistency.ReadOptions) (interface{}, error) {
	return h.lookup(query)
}

func newNALookupKVService(t *testing.T) (KVServicer, *statemachine.DiskBackend) {
	t.Helper()
	store := memorystorage.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	backend := statemachine.NewDiskBackend(store)
	return NewKVService(&naLookupHost{backend: backend, boxLookup: true}, 1), backend
}

// TestAbsentKeyIsNotFoundOnNALookupPath is the go-qrdb#214 regression.
//
// Before the fix every read verb below answered a missing key with a
// successful read of an empty value, so Exists was true for every key on
// raft.sm_type disk and highperf — including keys that were never written
// and keys that had just been deleted.
func TestAbsentKeyIsNotFoundOnNALookupPath(t *testing.T) {
	ctx := context.Background()

	assertNotFound := func(t *testing.T, svc KVServicer, key string) {
		t.Helper()
		var knf *KeyNotFoundError

		got, err := svc.Get(ctx, key)
		if !errors.As(err, &knf) {
			t.Errorf("Get(%q) = %q, err=%v; want KeyNotFoundError", key, got, err)
		}
		got, err = svc.GetStale(ctx, key)
		if !errors.As(err, &knf) {
			t.Errorf("GetStale(%q) = %q, err=%v; want KeyNotFoundError", key, got, err)
		}
		got, err = svc.GetWithConsistency(ctx, key, consistency.ReadOptions{})
		if !errors.As(err, &knf) {
			t.Errorf("GetWithConsistency(%q) = %q, err=%v; want KeyNotFoundError", key, got, err)
		}
		exists, err := svc.ExistsWithConsistency(ctx, key, consistency.ReadOptions{})
		if err != nil {
			t.Errorf("ExistsWithConsistency(%q): %v", key, err)
		}
		if exists {
			t.Errorf("ExistsWithConsistency(%q) = true; want false", key)
		}
	}

	t.Run("never written", func(t *testing.T) {
		svc, _ := newNALookupKVService(t)
		assertNotFound(t, svc, "never-written")
	})

	// A tombstone must read the same as a key that was never there. The
	// engines behind these adapters remove the entry outright rather than
	// keeping a marker, so this reaches the service layer through the same
	// boxed typed nil — but it is the case the issue's probe called out
	// separately and it is asserted separately.
	t.Run("after delete", func(t *testing.T) {
		svc, backend := newNALookupKVService(t)
		if err := backend.Put([]byte("probe"), []byte("v1")); err != nil {
			t.Fatalf("seed Put: %v", err)
		}
		if got, err := svc.Get(ctx, "probe"); err != nil || string(got) != "v1" {
			t.Fatalf("Get after seed = %q err=%v; want %q", got, err, "v1")
		}
		if err := backend.Delete([]byte("probe")); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		assertNotFound(t, svc, "probe")
	})

	// The other half of the promise, and the reason the fix keys on nil-ness
	// rather than on length: a key holding an empty value is PRESENT. Every
	// engine returns a defensively copied make([]byte, 0) for it, which is
	// non-nil, so it stays distinguishable from an absent key.
	t.Run("empty value is present", func(t *testing.T) {
		svc, backend := newNALookupKVService(t)
		if err := backend.Put([]byte("empty"), []byte{}); err != nil {
			t.Fatalf("seed Put: %v", err)
		}
		for _, tc := range []struct {
			name string
			read func() ([]byte, error)
		}{
			{"Get", func() ([]byte, error) { return svc.Get(ctx, "empty") }},
			{"GetStale", func() ([]byte, error) { return svc.GetStale(ctx, "empty") }},
			{"GetWithConsistency", func() ([]byte, error) {
				return svc.GetWithConsistency(ctx, "empty", consistency.ReadOptions{})
			}},
		} {
			got, err := tc.read()
			if err != nil {
				t.Errorf("%s(empty): %v; an empty value is a present key, not a missing one", tc.name, err)
			}
			if len(got) != 0 {
				t.Errorf("%s(empty) = %q; want an empty value", tc.name, got)
			}
		}
		exists, err := svc.ExistsWithConsistency(ctx, "empty", consistency.ReadOptions{})
		if err != nil || !exists {
			t.Errorf("ExistsWithConsistency(empty) = %v err=%v; want true", exists, err)
		}
	})
}
