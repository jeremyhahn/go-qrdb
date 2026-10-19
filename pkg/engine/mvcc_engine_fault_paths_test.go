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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// The documents the MVCC write-path fault tests index. user:1 holds
// faultDocA and user:2 holds faultDocB before every case runs; faultDocC is
// held by nobody.
const (
	faultDocA = `{"email":"a@example.com"}`
	faultDocB = `{"email":"b@example.com"}`
	faultDocC = `{"email":"c@example.com"}`
)

// mvccWriteCase is one write the MVCC engine applies through a distinct
// handler: each base-KV, MVCC, guarded, transactional and batched write.
type mvccWriteCase struct {
	name  string
	entry func(idx uint64, value string) sm.Entry
	// deletes marks a case that removes user:2 rather than writing value.
	deletes bool
}

func mvccOpsCmd(t *testing.T, op byte, key, value string) []byte {
	t.Helper()
	var v []byte
	if value != "" {
		v = []byte(value)
	}
	return mustEncode(protocol.EncodeOp(op, []byte(key), v, nil, 0))
}

func mvccWriteCases(t *testing.T) []mvccWriteCase {
	t.Helper()
	txn := func(op byte, key string) func(uint64, string) sm.Entry {
		return func(idx uint64, value string) sm.Entry {
			ops := [][]byte{mvccOpsCmd(t, op, key, value)}
			return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeTransaction(nil, ops, nil))}
		}
	}
	batch := func(op byte, key string) func(uint64, string) sm.Entry {
		return func(idx uint64, value string) sm.Entry {
			ops := [][]byte{mvccOpsCmd(t, op, key, value)}
			return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeBatch(ops))}
		}
	}
	return []mvccWriteCase{
		{name: "base put", entry: func(idx uint64, v string) sm.Entry { return kvPutEntry(idx, "user:3", v) }},
		{name: "base put-if-absent", entry: func(idx uint64, v string) sm.Entry { return baseKVPutIfAbsentEntry(idx, "user:3", v) }},
		{name: "mvcc put", entry: func(idx uint64, v string) sm.Entry { return mvccPutEntry(idx, []byte("user:3"), []byte(v), 0) }},
		{name: "mvcc put-if-absent", entry: func(idx uint64, v string) sm.Entry {
			return sm.Entry{Index: idx, Cmd: mustEncode(protocol.EncodeMVCCPutIfAbsent([]byte("user:3"), []byte(v), 0))}
		}},
		{name: "compare-and-swap", entry: func(idx uint64, v string) sm.Entry {
			return mvccCompareAndSwapEntry(idx, "user:2", faultDocB, v)
		}},
		{name: "transaction put", entry: txn(protocol.OpMVCCPut, "user:3")},
		{name: "batch put", entry: batch(protocol.OpMVCCPut, "user:3")},
		{name: "mvcc delete", deletes: true, entry: func(idx uint64, _ string) sm.Entry { return mvccDeleteEntry(idx, []byte("user:2")) }},
		{name: "base delete", deletes: true, entry: func(idx uint64, _ string) sm.Entry { return kvDeleteEntry(idx, "user:2") }},
		{name: "transaction delete", deletes: true, entry: txn(protocol.OpMVCCDelete, "user:2")},
		{name: "batch delete", deletes: true, entry: batch(protocol.OpMVCCDelete, "user:2")},
	}
}

// newIndexedFaultEngine opens an MVCC engine over stg and, when indexed is
// set, creates a unique index on "email" before seeding user:1 and user:2.
func newIndexedFaultEngine(t *testing.T, stg storage.Storage, indexed bool) *MVCCEngine {
	t.Helper()
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	var entries []sm.Entry
	if indexed {
		entries = append(entries, sm.Entry{Index: 1, Cmd: mustEncode(protocol.EncodeIndexCreateOpts(
			"by_email", "email", protocol.IndexCreateOptions{Unique: true}))})
	}
	entries = append(entries,
		baseKVPutIfAbsentEntry(2, "user:1", faultDocA),
		baseKVPutIfAbsentEntry(3, "user:2", faultDocB))
	applyMVCCEntries(t, eng, entries)
	return eng
}

func newMemoryBacked(t *testing.T) *memory.MemoryStorage {
	t.Helper()
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })
	return inner
}

// A write that would give a second record the unique value another record
// holds is refused as a CONFLICT application error on every write path, and
// changes nothing: it is not a fatal apply error, which would stall the group.
func TestMVCCEngine_UniqueConflictIsAnAppErrorOnEveryWritePath(t *testing.T) {
	for _, tc := range mvccWriteCases(t) {
		if tc.deletes {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			eng := newIndexedFaultEngine(t, newMemoryBacked(t), true)
			before := eng.Store().CurrentRevision()

			results := applyMVCCEntries(t, eng, []sm.Entry{tc.entry(10, faultDocA)})

			_, kind, ok := protocol.DecodeAppErrorKind(results[0].Data)
			if !ok || kind != protocol.AppErrorConflict {
				t.Fatalf("result data = %q, want a conflict application error", results[0].Data)
			}
			if after := eng.Store().CurrentRevision(); after != before {
				t.Fatalf("revision %d -> %d: a refused write must change nothing", before, after)
			}
			got, err := eng.Store().Get([]byte("user:2"))
			if err != nil || string(got.Value) != faultDocB {
				t.Fatalf("user:2 = %v, %v; want it untouched", got, err)
			}
		})
	}
}

// A storage fault while staging a write is fatal on every write path: the
// error reaches the caller of Update, typed as the injected fault, rather than
// being folded into a result. Run with and without a secondary index, so the
// fault lands in the index update as well as in the record write.
func TestMVCCEngine_BatchWriteFaultIsFatalOnEveryWritePath(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		for _, tc := range mvccWriteCases(t) {
			name := tc.name
			if indexed {
				name += " indexed"
			}
			t.Run(name, func(t *testing.T) {
				stg := &failingStorage{Storage: newMemoryBacked(t)}
				eng := newIndexedFaultEngine(t, stg, indexed)

				stg.failBatchSet = true
				err := eng.Update(context.Background(), []sm.Entry{tc.entry(10, faultDocC)}, make([]sm.Result, 1))
				stg.failBatchSet = false

				var injected *injectedError
				if !errors.As(err, &injected) {
					t.Fatalf("Update = %v, want the injected batch fault", err)
				}
			})
		}
	}
}

// A read of the key's current value that fails is fatal on every write path,
// unconditional ones included. A guard that took the fault for "absent" would
// overwrite the holder; an unconditional put or delete that took it for "no
// previous value" would leave the previous value's index entries behind on
// this replica alone, while peers that read it retract them.
func TestMVCCEngine_ReadFaultIsFatalOnEveryWritePath(t *testing.T) {
	for _, tc := range mvccWriteCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			stg := &mvccReadFaultStorage{Storage: newMemoryBacked(t)}
			eng := newIndexedFaultEngine(t, stg, true)
			before := eng.Store().CurrentRevision()

			stg.armed = true
			err := eng.Update(context.Background(), []sm.Entry{tc.entry(10, faultDocC)}, make([]sm.Result, 1))
			stg.armed = false

			if !errors.Is(err, errMVCCReadFault) {
				t.Fatalf("Update = %v, want the read fault propagated", err)
			}
			if after := eng.Store().CurrentRevision(); after != before {
				t.Fatalf("revision %d -> %d: a write that faulted must change nothing", before, after)
			}
		})
	}
}

// A transaction whose compare cannot be read is fatal before either branch is
// chosen: deciding the failure branch on a read fault would run writes the
// proposer never asked for.
func TestMVCCEngine_TransactionCompareReadFaultIsFatal(t *testing.T) {
	stg := &mvccReadFaultStorage{Storage: newMemoryBacked(t)}
	eng := newIndexedFaultEngine(t, stg, false)

	compares := [][]byte{mustEncode(protocol.EncodeCompare(
		[]byte("user:2"), byte(service.CompareValue), byte(service.CompareEqual), []byte(faultDocB), 0, 0, 0))}
	failure := [][]byte{mvccOpsCmd(t, protocol.OpMVCCPut, "user:9", faultDocC)}
	entry := sm.Entry{Index: 10, Cmd: mustEncode(protocol.EncodeTransaction(compares, nil, failure))}

	stg.armed = true
	err := eng.Update(context.Background(), []sm.Entry{entry}, make([]sm.Result, 1))
	stg.armed = false

	if !errors.Is(err, errMVCCReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated", err)
	}
	if _, gerr := eng.Store().Get([]byte("user:9")); gerr == nil {
		t.Fatal("the failure branch ran on a compare read fault")
	}
}
