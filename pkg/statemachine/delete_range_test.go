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

package statemachine

import (
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// DecodeDeleteRange / InvalidRangeError / rangeListPrefix
// ---------------------------------------------------------------------------

func TestDecodeDeleteRange_Bounded(t *testing.T) {
	cmd := mustEncode(protocol.EncodeDeleteRange([]byte("a"), []byte("c")))
	start, end, err := DecodeDeleteRange(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeDeleteRange: %v", err)
	}
	if string(start) != "a" || string(end) != "c" {
		t.Fatalf("got [%q, %q), want [\"a\", \"c\")", start, end)
	}
}

func TestDecodeDeleteRange_EmptyEndIsUnbounded(t *testing.T) {
	cmd := mustEncode(protocol.EncodeDeleteRange([]byte("a"), nil))
	start, end, err := DecodeDeleteRange(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeDeleteRange: %v", err)
	}
	if string(start) != "a" || end != nil {
		t.Fatalf("got [%q, %q), want [\"a\", nil)", start, end)
	}
}

func TestDecodeDeleteRange_InvalidBounds(t *testing.T) {
	cases := map[string]struct {
		start, end []byte
		want       error
	}{
		"empty start":      {start: nil, end: []byte("b"), want: storage.ErrEmptyKey},
		"end equals start": {start: []byte("b"), end: []byte("b"), want: storage.ErrInvalidRange},
		"end below start":  {start: []byte("c"), end: []byte("b"), want: storage.ErrInvalidRange},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := mustEncode(protocol.EncodeOp(protocol.OpDeleteRange, tc.start, nil, tc.end, 0))
			_, _, err := DecodeDeleteRange(cmd[1:])
			var rangeErr *InvalidRangeError
			if !errors.As(err, &rangeErr) {
				t.Fatalf("err = %v, want *InvalidRangeError", err)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}

func TestDecodeDeleteRange_Undecodable(t *testing.T) {
	if _, _, err := DecodeDeleteRange([]byte{0xff}); err == nil {
		t.Fatal("DecodeDeleteRange accepted a truncated payload")
	}
}

func TestInvalidRangeError(t *testing.T) {
	err := &InvalidRangeError{Err: storage.ErrInvalidRange}
	if err.Error() != "statemachine: invalid range: "+storage.ErrInvalidRange.Error() {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatal("InvalidRangeError does not unwrap to its sentinel")
	}
	if err.Refusal() == "" {
		t.Fatal("Refusal() is empty")
	}
	if _, ok := protocol.AsRefusalError(err); !ok {
		t.Fatal("InvalidRangeError is not classified as a refusal")
	}
}

func TestRangeListPrefix(t *testing.T) {
	cases := map[string]struct {
		start, end []byte
		want       string
	}{
		"shared prefix":     {[]byte("t/db/a"), []byte("t/db0"), "t/db"},
		"prefix successor":  {[]byte("t/db/"), []byte("t/db0"), "t/db"},
		"nothing shared":    {[]byte("a"), []byte("b"), ""},
		"unbounded end":     {[]byte("t/db/"), nil, ""},
		"start is a prefix": {[]byte("ab"), []byte("abc"), "ab"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := rangeListPrefix(tc.start, tc.end); got != tc.want {
				t.Fatalf("rangeListPrefix(%q, %q) = %q, want %q", tc.start, tc.end, got, tc.want)
			}
		})
	}
}

// A listing fault is returned, not swallowed into an empty range: a replica
// that could not list must not report the keys deleted.
func TestDeleteRangeOps_ListFaultIsReturned(t *testing.T) {
	boom := errors.New("disk")
	payload := mustEncode(protocol.EncodeDeleteRange([]byte("a"), []byte("b")))[1:]
	_, _, refused, err := deleteRangeOps(payload, func(string) ([]string, error) { return nil, boom })
	if refused || !errors.Is(err, boom) {
		t.Fatalf("deleteRangeOps = (refused %v, %v), want the listing fault", refused, err)
	}
}

// ---------------------------------------------------------------------------
// Conformance: every state machine applies OpDeleteRange alike
// ---------------------------------------------------------------------------

// rangeTarget is one state machine under the range-delete contract, with the
// watch manager it notifies.
type rangeTarget struct {
	conformanceTarget
	watch *mockWatchManager
}

// rangeTargets builds every state machine that can serve a node's sm_type:
// KVStateMachine on each backend an sm_type selects -- the disk backend over
// each storage raft.sm_storage selects -- and the three standalone state
// machines this package also implements.
func rangeTargets() map[string]func(t *testing.T) rangeTarget {
	targets := map[string]func(t *testing.T) rangeTarget{
		"RegularStateMachine": func(t *testing.T) rangeTarget {
			wm := &mockWatchManager{}
			r := NewRegularStateMachine(WithRegularWatchManager(wm))
			return rangeTarget{conformanceTarget{updaterWithContext(r.Update), lookupWithContext(r.Lookup), r.handlers}, wm}
		},
		"FastStateMachine": func(t *testing.T) rangeTarget {
			wm := &mockWatchManager{}
			f := NewFastStateMachine(1, 1, newMemStorage(), WithWatchManager(wm))
			return rangeTarget{conformanceTarget{updaterWithContext(f.Update), lookupWithContext(f.Lookup), f.handlers}, wm}
		},
	}
	kvBackends := leaseBackends()
	for _, kind := range []string{"memory", "file", "pebble"} {
		kvBackends["disk/"+kind] = nil
	}
	for name, nb := range kvBackends {
		targets["KVStateMachine/"+name] = func(t *testing.T) rangeTarget {
			t.Helper()
			var b Backend
			if nb != nil {
				b = nb()
			} else {
				stg := newStorageFactory(StorageConfig{Root: t.TempDir(), Kind: name[len("disk/"):]})(1, 1)
				t.Cleanup(func() { _ = stg.Close() })
				b = NewDiskBackend(stg)
			}
			wm := &mockWatchManager{}
			k := NewKVStateMachine(b, WithKVWatchManager(wm))
			return rangeTarget{conformanceTarget{
				apply: func(t *testing.T, cmd []byte) (sm.Result, error) {
					t.Helper()
					results := make([]sm.Result, 1)
					err := k.Update([]sm.Entry{{Index: 1, Cmd: cmd}}, results)
					return results[0], err
				},
				lookup: func(t *testing.T, key []byte) []byte {
					t.Helper()
					v, err := k.Lookup(key)
					if err != nil {
						t.Fatalf("Lookup: %v", err)
					}
					if v == nil {
						return nil
					}
					return v.([]byte)
				},
				handlers: k.handlers,
			}, wm}
		}
	}
	return targets
}

// seed puts each key with the value "v-"+key, then forgets the watch events
// the puts produced.
func (rt rangeTarget) seed(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, err := rt.apply(t, buildKVCmd(kv.OpPut, []byte(k), []byte("v-"+k))); err != nil {
			t.Fatalf("seed Put %q: %v", k, err)
		}
	}
	rt.watch.mu.Lock()
	rt.watch.events = nil
	rt.watch.mu.Unlock()
}

// deleteRange applies OpDeleteRange over [start, end); an empty end is
// unbounded.
func (rt rangeTarget) deleteRange(t *testing.T, start, end string) sm.Result {
	t.Helper()
	var endBytes []byte
	if end != "" {
		endBytes = []byte(end)
	}
	res, err := rt.apply(t, mustEncode(protocol.EncodeDeleteRange([]byte(start), endBytes)))
	if err != nil {
		t.Fatalf("range delete [%q, %q): %v", start, end, err)
	}
	if _, refused := protocol.DecodeAppError(res.Data); refused {
		t.Fatalf("range delete [%q, %q) refused: %q", start, end, res.Data)
	}
	return res
}

// live returns which of keys are present.
func (rt rangeTarget) live(t *testing.T, keys ...string) []string {
	t.Helper()
	var present []string
	for _, k := range keys {
		if rt.lookup(t, []byte(k)) != nil {
			present = append(present, k)
		}
	}
	return present
}

// The range-delete contract: the keys in [start, end) are deleted and no
// other; watchers see one DELETE per deleted key, in key order, all at the
// one revision the entry applied at; the result reports how many were
// deleted. Bounds that are not a range, and a start in the reserved
// namespace, are refusals in the result -- never apply errors, which would
// stall the shard -- and delete nothing.
func TestStateMachines_DeleteRangeContract(t *testing.T) {
	seeded := []string{"a", "b", "bz", "c0", "d"}
	for name, build := range rangeTargets() {
		t.Run(name, func(t *testing.T) {
			t.Run("dispatches OpDeleteRange", func(t *testing.T) {
				if _, ok := build(t).handlers[protocol.OpDeleteRange]; !ok {
					t.Fatalf("%s has no handler for OpDeleteRange: a reclaim would be refused as an unknown opcode", name)
				}
			})

			t.Run("deletes exactly the range at one revision", func(t *testing.T) {
				rt := build(t)
				rt.seed(t, seeded...)
				res := rt.deleteRange(t, "b", "d")
				if res.Value != 3 {
					t.Fatalf("result Value = %d, want 3 keys deleted", res.Value)
				}
				if got := rt.live(t, seeded...); !slices.Equal(got, []string{"a", "d"}) {
					t.Fatalf("live keys = %v, want [a d]", got)
				}
				events := rt.watch.getEvents()
				var keys []string
				for _, ev := range events {
					if !ev.deleted || ev.value != nil {
						t.Fatalf("event %+v is not a delete", ev)
					}
					if ev.revision != events[0].revision {
						t.Fatalf("events at revisions %d and %d: a range delete is one revision", events[0].revision, ev.revision)
					}
					keys = append(keys, ev.key)
				}
				if !slices.Equal(keys, []string{"b", "bz", "c0"}) {
					t.Fatalf("delete events for %v, want [b bz c0] in key order", keys)
				}
			})

			t.Run("an empty end runs through the end of the keyspace", func(t *testing.T) {
				rt := build(t)
				rt.seed(t, seeded...)
				rt.deleteRange(t, "bz", "")
				if got := rt.live(t, seeded...); !slices.Equal(got, []string{"a", "b"}) {
					t.Fatalf("live keys = %v, want [a b]", got)
				}
			})

			t.Run("an empty range succeeds and notifies nobody", func(t *testing.T) {
				rt := build(t)
				rt.seed(t, seeded...)
				if res := rt.deleteRange(t, "x", "y"); res.Value != 0 {
					t.Fatalf("result Value = %d, want 0", res.Value)
				}
				if got := rt.live(t, seeded...); len(got) != len(seeded) {
					t.Fatalf("live keys = %v, want all of %v", got, seeded)
				}
				if events := rt.watch.getEvents(); len(events) != 0 {
					t.Fatalf("an empty range notified %v", events)
				}
			})

			t.Run("is what the same single deletes leave", func(t *testing.T) {
				ranged, single := build(t), build(t)
				ranged.seed(t, seeded...)
				single.seed(t, seeded...)
				ranged.deleteRange(t, "b", "c1")
				for _, k := range []string{"b", "bz", "c0"} {
					if _, err := single.apply(t, buildKVCmd(kv.OpDelete, []byte(k), nil)); err != nil {
						t.Fatalf("Delete %q: %v", k, err)
					}
				}
				for _, k := range seeded {
					if got, want := ranged.lookup(t, []byte(k)), single.lookup(t, []byte(k)); string(got) != string(want) {
						t.Fatalf("%q = %q after the range delete, %q after single deletes", k, got, want)
					}
				}
			})

			t.Run("invalid bounds are a refusal that deletes nothing", func(t *testing.T) {
				rt := build(t)
				rt.seed(t, seeded...)
				res, err := rt.apply(t, mustEncode(protocol.EncodeOp(protocol.OpDeleteRange, []byte("c"), nil, []byte("a"), 0)))
				if err != nil {
					t.Fatalf("invalid bounds must be answered, not fatal: %v", err)
				}
				if _, refused := protocol.DecodeAppError(res.Data); !refused {
					t.Fatalf("Result.Data = %x, want a refusal", res.Data)
				}
				if got := rt.live(t, seeded...); len(got) != len(seeded) {
					t.Fatalf("a refused range delete deleted keys: live %v", got)
				}
			})

			t.Run("a reserved start is a refusal", func(t *testing.T) {
				rt := build(t)
				rt.seed(t, seeded...)
				res, err := rt.apply(t, mustEncode(protocol.EncodeOp(protocol.OpDeleteRange, []byte{0x00}, nil, []byte("z"), 0)))
				if err != nil {
					t.Fatalf("a reserved start must be answered, not fatal: %v", err)
				}
				if _, refused := protocol.DecodeAppError(res.Data); !refused {
					t.Fatalf("Result.Data = %x, want a refusal", res.Data)
				}
				if got := rt.live(t, seeded...); len(got) != len(seeded) {
					t.Fatalf("a refused range delete deleted keys: live %v", got)
				}
			})

			t.Run("a malformed payload is a decode fault", func(t *testing.T) {
				if _, err := build(t).apply(t, []byte{protocol.OpDeleteRange, 0xff}); err == nil {
					t.Fatal("a malformed range delete applied without error")
				}
			})
		})
	}
}

// A key a range delete removes leaves its lease, as a single delete's does:
// revoking the lease afterwards must not delete a key written again without
// it, and the lease no longer lists the key.
func TestKVStateMachine_DeleteRangeReleasesLeasedKeys(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			id := s.grant(30)
			s.putLeased("r/a", "1", id)
			s.putLeased("r/b", "2", id)
			s.putLeased("keep", "3", id)

			res := s.apply(mustEncode(protocol.EncodeDeleteRange([]byte("r/"), []byte("r0"))))
			if _, refused := protocol.DecodeAppError(res.Data); refused {
				t.Fatalf("range delete refused: %q", res.Data)
			}
			ttl, err := s.k.Lookup(lease.LeaseTimeToLiveQuery{ID: id})
			if err != nil {
				t.Fatalf("TimeToLive: %v", err)
			}
			if keys := ttl.(*lease.Lease).Keys; len(keys) != 1 {
				t.Fatalf("lease keys = %v, want only the key outside the range", keys)
			}

			s.putLeased("r/a", "again", lease.NoLease)
			s.revoke(id)
			if string(s.get("r/a")) != "again" {
				t.Fatal("revoking the lease deleted a key the range delete had released from it")
			}
			if s.get("keep") != nil {
				t.Fatal("a key still bound to the revoked lease survived it")
			}
		})
	}
}
