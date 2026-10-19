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

package engine

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// kvWriteCase is one write the KV engine applies through a distinct handler.
// It writes user:3 (absent) or replaces or removes user:2 (holding faultDocB).
type kvWriteCase struct {
	name    string
	key     string
	entry   func(idx uint64) sm.Entry
	deletes bool
}

func kvWriteCases(t *testing.T) []kvWriteCase {
	t.Helper()
	txn := func(op byte, key string) func(uint64) sm.Entry {
		return func(idx uint64) sm.Entry {
			ops := [][]byte{mvccOpsCmd(t, op, key, faultDocCIf(op))}
			return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeTransaction(nil, ops, nil))}
		}
	}
	batch := func(op byte, key string) func(uint64) sm.Entry {
		return func(idx uint64) sm.Entry {
			ops := [][]byte{mvccOpsCmd(t, op, key, faultDocCIf(op))}
			return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeBatch(ops))}
		}
	}
	return []kvWriteCase{
		{name: "put", key: "user:3", entry: func(idx uint64) sm.Entry { return kvPutEntry(idx, "user:3", faultDocC) }},
		{name: "delete", key: "user:2", deletes: true, entry: func(idx uint64) sm.Entry { return kvDeleteEntry(idx, "user:2") }},
		{name: "put-if-absent", key: "user:3", entry: func(idx uint64) sm.Entry { return kvPutIfAbsentEntry(idx, "user:3", faultDocC) }},
		{name: "compare-and-swap", key: "user:2", entry: func(idx uint64) sm.Entry {
			return kvCompareAndSwapEntry(idx, "user:2", faultDocB, faultDocC)
		}},
		{name: "transaction put", key: "user:3", entry: txn(protocol.OpMVCCPut, "user:3")},
		{name: "batch put", key: "user:3", entry: batch(protocol.OpFSMPut, "user:3")},
		{name: "compare-and-delete", key: "user:2", deletes: true, entry: func(idx uint64) sm.Entry {
			return kvCompareAndDeleteEntry(idx, "user:2", faultDocB)
		}},
		{name: "transaction delete", key: "user:2", deletes: true, entry: txn(protocol.OpMVCCDelete, "user:2")},
		{name: "batch delete", key: "user:2", deletes: true, entry: batch(protocol.OpFSMDelete, "user:2")},
	}
}

// faultDocCIf is the value an op carries: faultDocC for a put, none for a
// delete.
func faultDocCIf(op byte) string {
	if op == protocol.OpMVCCPut || op == protocol.OpFSMPut {
		return faultDocC
	}
	return ""
}

// newIndexedFaultKVEngine opens a KV engine over stg with a watch hub and,
// when indexed is set, a unique index on "email", then seeds user:1 and
// user:2.
func newIndexedFaultKVEngine(t *testing.T, stg storage.Storage, indexed bool) *KVEngine {
	t.Helper()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	eng := NewKVEngine(stg, WithKVWatchHub(hub))
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	var entries []sm.Entry
	if indexed {
		entries = append(entries, sm.Entry{Index: 1, Cmd: mustEncode(protocol.EncodeIndexCreateOpts(
			"by_email", "email", protocol.IndexCreateOptions{Unique: true}))})
	}
	entries = append(entries,
		kvPutIfAbsentEntry(2, "user:1", faultDocA),
		kvPutIfAbsentEntry(3, "user:2", faultDocB))
	applyKVEntries(t, eng, entries)
	return eng
}

// A storage fault while staging or committing a write is fatal on every KV
// write path, with and without a secondary index: the error reaches the
// caller of Update typed as the injected fault, and the write is not visible.
func TestKVEngine_BatchFaultIsFatalOnEveryWritePath(t *testing.T) {
	type fault struct {
		name string
		arm  func(*failingStorage, bool)
		// applies reports whether the fault is reachable by the case.
		applies func(kvWriteCase) bool
	}
	faults := []fault{
		{"set", func(s *failingStorage, on bool) { s.failBatchSet = on }, func(c kvWriteCase) bool { return !c.deletes }},
		{"delete", func(s *failingStorage, on bool) { s.failBatchDelete = on }, func(c kvWriteCase) bool { return c.deletes }},
		{"commit", func(s *failingStorage, on bool) { s.failBatchCommit = on }, func(kvWriteCase) bool { return true }},
	}
	for _, indexed := range []bool{false, true} {
		for _, f := range faults {
			for _, tc := range kvWriteCases(t) {
				if !f.applies(tc) {
					continue
				}
				name := f.name + "/" + tc.name
				if indexed {
					name += " indexed"
				}
				t.Run(name, func(t *testing.T) {
					stg := &failingStorage{Storage: newMemoryBacked(t)}
					eng := newIndexedFaultKVEngine(t, stg, indexed)
					want, _ := eng.Lookup(context.Background(), tc.key)

					f.arm(stg, true)
					err := eng.Update(context.Background(), []sm.Entry{tc.entry(10)}, make([]sm.Result, 1))
					f.arm(stg, false)

					var injected *injectedError
					if !errors.As(err, &injected) {
						t.Fatalf("Update = %v, want the injected batch fault", err)
					}
					got, _ := eng.Lookup(context.Background(), tc.key)
					if string(asBytes(got)) != string(asBytes(want)) {
						t.Fatalf("%s = %q after a faulted write, want %q", tc.key, asBytes(got), asBytes(want))
					}
				})
			}
		}
	}
}

// A read of the key's previous value that fails is fatal on every KV write
// path. A write that took the fault for "no previous value" would leave the
// previous value's index entries behind on this replica alone, while peers
// that read it retract them.
func TestKVEngine_PreviousValueReadFaultIsFatal(t *testing.T) {
	for _, tc := range kvWriteCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			stg := &readFaultStorage{Storage: newMemoryBacked(t)}
			eng := newIndexedFaultKVEngine(t, stg, true)
			want, _ := eng.Lookup(context.Background(), tc.key)

			stg.failKey = tc.key
			err := eng.Update(context.Background(), []sm.Entry{tc.entry(10)}, make([]sm.Result, 1))
			stg.failKey = ""

			if !errors.Is(err, errReadFault) {
				t.Fatalf("Update = %v, want the read fault propagated", err)
			}
			got, _ := eng.Lookup(context.Background(), tc.key)
			if string(asBytes(got)) != string(asBytes(want)) {
				t.Fatalf("%s = %q after a faulted write, want %q", tc.key, asBytes(got), asBytes(want))
			}
		})
	}
}

// PutWithIndexes and DeleteWithIndexes return a fault reading the key's
// previous value and write nothing: neither the value nor any index entry.
// Taking the fault for "no previous value" would leave the previous value's
// index entries pointing at a value that no longer carries those fields. An
// absent key is still "no previous value" and is written.
func TestKVEngine_WithIndexesPreviousValueReadFault(t *testing.T) {
	put := func(key, doc string) func(*KVEngine) error {
		return func(e *KVEngine) error { return e.PutWithIndexes([]byte(key), []byte(doc)) }
	}
	del := func(key string) func(*KVEngine) error {
		return func(e *KVEngine) error { return e.DeleteWithIndexes([]byte(key)) }
	}
	cases := []struct {
		name       string
		failKey    string
		write      func(*KVEngine) error
		wantErr    error
		wantValues map[string]string
		wantIndex  map[string][]string
	}{
		{
			name: "put faulted", failKey: "user:2", write: put("user:2", faultDocC), wantErr: errReadFault,
			wantValues: map[string]string{"user:2": faultDocB},
			wantIndex:  map[string][]string{"b@example.com": {"user:2"}, "c@example.com": nil},
		},
		{
			name: "delete faulted", failKey: "user:2", write: del("user:2"), wantErr: errReadFault,
			wantValues: map[string]string{"user:2": faultDocB},
			wantIndex:  map[string][]string{"b@example.com": {"user:2"}},
		},
		{
			name: "put absent key", write: put("user:3", faultDocC),
			wantValues: map[string]string{"user:2": faultDocB, "user:3": faultDocC},
			wantIndex:  map[string][]string{"b@example.com": {"user:2"}, "c@example.com": {"user:3"}},
		},
		{
			name: "delete absent key", write: del("user:9"),
			wantValues: map[string]string{"user:2": faultDocB, "user:9": ""},
			wantIndex:  map[string][]string{"b@example.com": {"user:2"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stg := &readFaultStorage{Storage: newMemoryBacked(t)}
			eng := newIndexedFaultKVEngine(t, stg, true)

			stg.failKey = tc.failKey
			err := tc.write(eng)
			stg.failKey = ""

			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("write = %v, want %v", err, tc.wantErr)
			}
			for key, want := range tc.wantValues {
				got, _ := eng.Lookup(context.Background(), key)
				if string(asBytes(got)) != want {
					t.Errorf("%s = %q, want %q", key, asBytes(got), want)
				}
			}
			for email, want := range tc.wantIndex {
				if got := emailIndexed(t, eng, email); !slices.Equal(got, want) {
					t.Errorf("by_email[%s] = %v, want %v", email, got, want)
				}
			}
		})
	}
}

// emailIndexed returns the keys the by_email index holds for email.
func emailIndexed(t *testing.T, eng *KVEngine, email string) []string {
	t.Helper()
	res, err := eng.Lookup(context.Background(), index.IndexQueryRequest{IndexName: "by_email", Value: []byte(email)})
	if err != nil {
		t.Fatalf("Lookup by_email[%s]: %v", email, err)
	}
	var keys []string
	for _, k := range res.([][]byte) {
		keys = append(keys, string(k))
	}
	return keys
}

// asBytes reads a key Lookup's result, nil for an absent key.
func asBytes(v interface{}) []byte {
	b, _ := v.([]byte)
	return b
}
