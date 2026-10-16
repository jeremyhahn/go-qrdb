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
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// The Pebble state machine applies OpDeleteRange as OpDelete applied to each
// key in the range, as both pkg/engine engines do: a delete is more than the
// key going away, and a storage range tombstone did only that.

func putEntries(t *testing.T, first uint64, keys ...string) []sm.Entry {
	t.Helper()
	entries := make([]sm.Entry, len(keys))
	for i, k := range keys {
		entries[i] = sm.Entry{Index: first + uint64(i), Cmd: mustEncode(kv.EncodePut([]byte(k), []byte(`{"status":"active"}`)))}
	}
	return entries
}

func TestPebbleStateMachine_DeleteRange_DeletesAndCountsTheRange(t *testing.T) {
	cases := map[string]struct {
		start, end []byte
		want       uint64
		survivors  []string
		gone       []string
	}{
		"bounded keeps its exclusive end":  {[]byte("app/b"), []byte("app/d"), 2, []string{"app/a", "app/d", "zz"}, []string{"app/b", "app/c"}},
		"nil end reaches the keyspace end": {[]byte("app/c"), nil, 3, []string{"app/a", "app/b"}, []string{"app/c", "app/d", "zz"}},
		"an empty range deletes nothing":   {[]byte("b"), []byte("c"), 0, []string{"app/a", "app/d", "zz"}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := openExtendedStore(t)
			applyExtEntries(t, s, putEntries(t, 1, "app/a", "app/b", "app/c", "app/d", "zz"))

			res := applyExtEntries(t, s, []sm.Entry{{Index: 6, Cmd: mustEncode(protocol.EncodeDeleteRange(tc.start, tc.end))}})
			if res[0].Value != tc.want || len(res[0].Data) != 0 {
				t.Fatalf("result = %+v, want %d deleted and no error", res[0], tc.want)
			}
			for _, k := range tc.survivors {
				if lookupExtKey(t, s, k) == nil {
					t.Errorf("%s is outside the range and was deleted", k)
				}
			}
			for _, k := range tc.gone {
				if got := lookupExtKey(t, s, k); got != nil {
					t.Errorf("%s survived the range delete: %q", k, got)
				}
			}
		})
	}
}

// TestPebbleStateMachine_DeleteRange_SeesTheSameApply checks the range is read
// through the apply batch: a key an earlier entry of the same apply wrote is
// deleted, and one a later entry writes is not.
func TestPebbleStateMachine_DeleteRange_SeesTheSameApply(t *testing.T) {
	s := openExtendedStore(t)
	applyExtEntries(t, s, putEntries(t, 1, "app/a"))

	entries := append(putEntries(t, 2, "app/pending"),
		sm.Entry{Index: 3, Cmd: mustEncode(protocol.EncodeDeleteRange([]byte("app/"), nil))})
	entries = append(entries, putEntries(t, 4, "app/later")...)
	res := applyExtEntries(t, s, entries)

	if res[1].Value != 2 {
		t.Fatalf("range delete counted %d keys, want app/a and app/pending", res[1].Value)
	}
	if lookupExtKey(t, s, "app/pending") != nil || lookupExtKey(t, s, "app/a") != nil {
		t.Fatal("a key the range covered survived")
	}
	if lookupExtKey(t, s, "app/later") == nil {
		t.Fatal("a write applied after the range delete was deleted by it")
	}
}

func TestPebbleStateMachine_DeleteRange_RemovesIndexEntries(t *testing.T) {
	s := openExtendedStore(t)
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})
	applyExtEntries(t, s, putEntries(t, 2, "user/1", "user/2", "zone/1"))

	applyExtEntries(t, s, []sm.Entry{{Index: 5, Cmd: mustEncode(protocol.EncodeDeleteRange([]byte("user/"), storage.PrefixEnd([]byte("user/"))))}})

	got := indexQueryKeys(t, s, "by_status", "active")
	if len(got) != 1 || !containsKey(got, "zone/1") {
		t.Fatalf("IndexQuery(by_status, active) after the range delete = %v, want only zone/1", got)
	}
}

// TestPebbleStateMachine_DeleteRange_ReleasesLeaseBindings checks a deleted
// key no longer belongs to its lease, so a later revoke does not count -- or
// delete -- a key the range already removed.
func TestPebbleStateMachine_DeleteRange_ReleasesLeaseBindings(t *testing.T) {
	s, lr := openStoreWithLessor(t)
	grant := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
	leaseID := int64(grant[0].Value)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("app/leased"), []byte("v"), leaseID))}})
	if lr.LeaseOf([]byte("app/leased")) != leaseID {
		t.Fatal("the put did not bind the key to its lease")
	}

	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeDeleteRange([]byte("app/"), nil))}})

	if got := lr.LeaseOf([]byte("app/leased")); got != lease.NoLease {
		t.Fatalf("the deleted key is still bound to lease %d", got)
	}
	revoke := applyExtEntries(t, s, []sm.Entry{{Index: 4, Cmd: mustEncode(protocol.EncodeLeaseRevoke(leaseID))}})
	if revoke[0].Value != 0 {
		t.Fatalf("revoke removed %d keys, want none: the range delete released the binding", revoke[0].Value)
	}
}

func TestPebbleStateMachine_DeleteRange_NotifiesWatchers(t *testing.T) {
	s, hub := openStoreWithHub(t)
	applyExtEntries(t, s, putEntries(t, 1, "app/a", "app/b"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchers := make(map[string]watch.Watcher, 2)
	for _, k := range []string{"app/a", "app/b"} {
		w, err := hub.Watch(ctx, k, watch.WithPrevKV())
		if err != nil {
			t.Fatalf("Watch(%s): %v", k, err)
		}
		defer func() { _ = w.Close() }()
		watchers[k] = w
	}

	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: mustEncode(protocol.EncodeDeleteRange([]byte("app/"), nil))}})

	for k, w := range watchers {
		select {
		case resp := <-w.Events():
			if len(resp.Events) != 1 || resp.Events[0].Type != watch.EventDelete || resp.Events[0].PrevKv == nil {
				t.Fatalf("%s: events %+v, want one delete carrying the previous value", k, resp.Events)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no delete event for a key the range removed", k)
		}
	}
}

// TestPebbleStateMachine_DeleteRange_RefusesBoundsThatAreNoRange checks bounds
// a proposer can encode but that describe no range are refused in the result
// -- every replica refuses the same entry -- rather than halting apply.
func TestPebbleStateMachine_DeleteRange_RefusesBoundsThatAreNoRange(t *testing.T) {
	s := openExtendedStore(t)
	applyExtEntries(t, s, putEntries(t, 1, "a", "b"))

	for i, cmd := range [][]byte{
		mustEncode(protocol.EncodeDeleteRange([]byte("b"), []byte("a"))),
		mustEncode(protocol.EncodeDeleteRange([]byte("a"), []byte("a"))),
		mustEncode(protocol.EncodeDeleteRange(nil, []byte("b"))),
	} {
		res := applyExtEntries(t, s, []sm.Entry{{Index: uint64(3 + i), Cmd: cmd}})
		if _, _, ok := protocol.DecodeAppErrorKind(res[0].Data); !ok {
			t.Fatalf("command %d: result %+v, want an encoded refusal", i, res[0])
		}
	}
	if lookupExtKey(t, s, "a") == nil || lookupExtKey(t, s, "b") == nil {
		t.Fatal("a refused range delete removed keys")
	}
}

func TestPebbleStateMachine_DeleteRange_UndecodableIsAnError(t *testing.T) {
	s := openExtendedStore(t)
	_, _, err := pebbleHandleDeleteRange(s, readBatch(t, s), []byte{0xff})
	var invalid *protocol.InvalidCommandError
	if !errors.As(err, &invalid) {
		t.Fatalf("pebbleHandleDeleteRange on a truncated payload = %T (%v), want *protocol.InvalidCommandError", err, err)
	}
}
