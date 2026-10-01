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

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
)

// This is go-qrdb#136's acceptance test: the guarded and transaction plane
// behaves IDENTICALLY on every raft state machine type.
//
// Each cell is a real state machine as its composition root builds it — the
// registry's own factories over each raft.sm_storage for the storage-generic
// ones, and the pebble and mvcc state machines as the daemon registers them —
// driven through the real service layer (KVService, TransactionService,
// BatchService) and the real guarded-batch adapter over an in-process host.
// Nothing between the caller and the state machine is a double, so what is
// measured is what a node answers.
//
// Every cell runs the same battery and writes every observable outcome to a
// transcript. Each outcome is asserted where it is produced, and then every
// transcript is compared with the default state machine's (pebble): a cell
// that answers one question differently fails even if both answers looked
// plausible on their own. The one documented difference — the mvcc state
// machine keeps revisions and so ACCEPTS a create_revision compare the others
// refuse — is asserted explicitly and kept out of the transcript.

// smCell is one state machine under test.
type smCell struct {
	name string
	// open builds a fresh, empty instance for replica. Instances of one cell
	// with different replica ids never share storage.
	open func(t *testing.T, replica uint64) interface{}
	// revisions marks the state machine that keeps MVCC revisions.
	revisions bool
	// registry marks a state machine from pkg/statemachine's registry.
	registry bool
}

// registrySMTypes are the non-default raft.sm_type values, in the order the
// matrix reports them. The storage-generic ones are expanded over every
// raft.sm_storage below; the test fails if the registry grows a type this list
// does not name, so a new state machine cannot skip the battery.
var registrySMTypes = []string{"memory", "standard", "concurrent", "fast", "disk", "highperf", "regular"}

func smCells(t *testing.T) []smCell {
	t.Helper()

	registered := statemachine.Names()
	if !equalStrings(sortedCopy(registered), sortedCopy(registrySMTypes)) {
		t.Fatalf("statemachine registry holds %v but this battery covers %v — add the new "+
			"sm_type here so it runs the conformance battery", registered, registrySMTypes)
	}

	cells := []smCell{
		{name: "pebble", open: openStoreSM(func() sm.DiskStateMachine { return store.NewPebbleStateMachine(&pebble.Options{}) })},
		{name: "mvcc", revisions: true, open: openStoreSM(func() sm.DiskStateMachine { return store.NewMVCCStateMachine(&pebble.Options{}) })},
	}
	for _, name := range registrySMTypes {
		desc, err := statemachine.Describe(name)
		if err != nil {
			t.Fatalf("Describe(%q): %v", name, err)
		}
		kinds := []string{""}
		if desc.StorageGeneric {
			kinds = []string{"pebble", "file", "memory"}
		}
		for _, kind := range kinds {
			cellName := name
			if kind != "" {
				cellName = name + "/" + kind
			}
			cells = append(cells, smCell{name: cellName, registry: true, open: func(t *testing.T, replica uint64) interface{} {
				t.Helper()
				createFn, err := statemachine.ResolveTypedWithStorage(name, "",
					statemachine.StorageConfig{Root: t.TempDir(), Kind: kind})
				if err != nil {
					t.Fatalf("ResolveTypedWithStorage(%q, %q): %v", name, kind, err)
				}
				return openMachine(t, createFn(1, replica))
			}})
		}
	}
	return cells
}

// openStoreSM opens one of pkg/store's pebble-native state machines in a
// fresh directory.
func openStoreSM(newSM func() sm.DiskStateMachine) func(t *testing.T, replica uint64) interface{} {
	return func(t *testing.T, _ uint64) interface{} {
		t.Helper()
		return openMachine(t, newSM())
	}
}

// openMachine opens a disk state machine and registers every instance's
// Close.
func openMachine(t *testing.T, machine interface{}) interface{} {
	t.Helper()
	if d, ok := machine.(sm.DiskStateMachine); ok {
		if _, err := d.Open(context.Background(), t.TempDir(), nil); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	t.Cleanup(func() { _ = machine.(backend.SMAdapter).Close(context.Background()) })
	return machine
}

// snapshotOf saves a snapshot through whichever snapshot shape the state
// machine has: prepared (concurrent and disk) or direct (regular).
func snapshotOf(t *testing.T, machine interface{}) []byte {
	t.Helper()
	ctx := context.Background()
	var buf bytes.Buffer
	switch m := machine.(type) {
	case interface {
		PrepareSnapshot() (interface{}, error)
		SaveSnapshot(context.Context, interface{}, io.Writer, <-chan struct{}) error
	}:
		snapCtx, err := m.PrepareSnapshot()
		if err != nil {
			t.Fatalf("PrepareSnapshot: %v", err)
		}
		if err := m.SaveSnapshot(ctx, snapCtx, &buf, nil); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
	case interface {
		SaveSnapshot(context.Context, io.Writer, <-chan struct{}) error
	}:
		if err := m.SaveSnapshot(ctx, &buf, nil); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
	default:
		t.Fatalf("%T has no recognised SaveSnapshot", machine)
	}
	return buf.Bytes()
}

func restoreInto(t *testing.T, machine interface{}, snap []byte) {
	t.Helper()
	r, ok := machine.(interface {
		RecoverFromSnapshot(context.Context, io.Reader, <-chan struct{}) error
	})
	if !ok {
		t.Fatalf("%T has no RecoverFromSnapshot", machine)
	}
	if err := r.RecoverFromSnapshot(context.Background(), bytes.NewReader(snap), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}
}

// planes is the service surface a node composes over one state machine.
type planes struct {
	host    service.Host
	kv      service.KVServicer
	tx      service.TransactionServicer
	batch   service.BatchServicer
	guarded kvstore.KVStore
}

func planesOver(t *testing.T, machine interface{}) planes {
	t.Helper()
	host := backend.NewLocalBackend(machine.(backend.SMAdapter))
	kv := service.NewKVService(host, 0)
	tx := service.NewTransactionService(host, 0)
	guarded, err := NewGuardedKVStoreAdapter(kv, service.NewIndexService(host, 0), tx)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	return planes{host: host, kv: kv, tx: tx, batch: service.NewBatchService(host, 0), guarded: guarded}
}

// transcript records a cell's observable outcomes, one line per question.
type transcript struct {
	t     *testing.T
	cell  string
	lines []string
}

// check records an outcome and fails when it is not the expected one.
func (tr *transcript) check(step string, got, want interface{}) {
	tr.t.Helper()
	line := fmt.Sprintf("%s = %v", step, got)
	tr.lines = append(tr.lines, line)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		tr.t.Errorf("%s: %s; want %v", tr.cell, line, want)
	}
}

func (tr *transcript) must(step string, err error) {
	tr.t.Helper()
	if err != nil {
		tr.t.Fatalf("%s: %s: %v", tr.cell, step, err)
	}
}

// value reads key through the plain KV plane, rendering absence distinctly
// from an empty value.
func value(ctx context.Context, t *testing.T, p planes, key string) string {
	t.Helper()
	ok, err := p.guarded.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists(%q): %v", key, err)
	}
	if !ok {
		return "<absent>"
	}
	v, err := p.kv.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return fmt.Sprintf("%q", v)
}

// battery drives every guarded and transactional verb through p and records
// the answers.
func battery(ctx context.Context, tr *transcript, p planes) {
	t := tr.t
	t.Helper()
	atomicKV := p.guarded.(kvstore.AtomicKVStore)
	multi := p.guarded.(kvstore.MultiGuardedBatchKVStore)

	// go-qrdb#555's boot-time probe: a real guarded batch, not a type check.
	ok, err := kvstore.ProbeGuardedBatch(ctx, multi)
	tr.must("ProbeGuardedBatch", err)
	tr.check("probe", ok, true)

	// --- single-key guarded verbs ---
	stored, err := atomicKV.PutIfAbsent(ctx, "owner", []byte("A"))
	tr.must("PutIfAbsent", err)
	tr.check("PutIfAbsent(absent)", stored, true)
	stored, err = atomicKV.PutIfAbsent(ctx, "owner", []byte("B"))
	tr.must("PutIfAbsent", err)
	tr.check("PutIfAbsent(present)", stored, false)
	tr.check("owner", value(ctx, t, p, "owner"), `"A"`)

	swapped, err := atomicKV.CompareAndSwap(ctx, "owner", []byte("stale"), []byte("B"))
	tr.must("CompareAndSwap", err)
	tr.check("CAS(stale)", swapped, false)
	swapped, err = atomicKV.CompareAndSwap(ctx, "nothing", []byte(""), []byte("B"))
	tr.must("CompareAndSwap", err)
	tr.check("CAS(absent)", swapped, false)
	tr.check("nothing", value(ctx, t, p, "nothing"), "<absent>")

	tr.must("Put(release)", p.kv.Put(ctx, "release", []byte("mine")))
	deleted, err := atomicKV.CompareAndDelete(ctx, "release", []byte("theirs"))
	tr.must("CompareAndDelete", err)
	tr.check("CAD(wrong)", deleted, false)
	deleted, err = atomicKV.CompareAndDelete(ctx, "release", []byte("mine"))
	tr.must("CompareAndDelete", err)
	tr.check("CAD(match)", deleted, true)
	tr.check("release", value(ctx, t, p, "release"), "<absent>")

	// --- guarded batches, fenced on keys the PLAIN KV plane wrote ---
	applied, err := multi.PutAllIfUnchanged(ctx, "owner", []byte("A"),
		[]kvstore.KV{{Key: "rec/1", Value: []byte("one")}, {Key: "idx/1", Value: []byte("rec/1")}})
	tr.must("PutAllIfUnchanged", err)
	tr.check("PutAllIfUnchanged(held)", applied, true)
	tr.check("rec/1", value(ctx, t, p, "rec/1"), `"one"`)
	tr.check("idx/1", value(ctx, t, p, "idx/1"), `"rec/1"`)

	applied, err = multi.PutAllIfUnchanged(ctx, "owner", []byte("B"), []kvstore.KV{{Key: "rec/2", Value: []byte("two")}})
	tr.must("PutAllIfUnchanged", err)
	tr.check("PutAllIfUnchanged(lost)", applied, false)
	tr.check("rec/2", value(ctx, t, p, "rec/2"), "<absent>")

	// Multi-guard, as role and user creation use it: a held claim AND a
	// must-be-absent name.
	guards := []kvstore.Guard{{Key: "owner", Value: []byte("A")}, {Key: "name/alice"}}
	applied, err = multi.PutAllIfAllUnchanged(ctx, guards, []kvstore.KV{{Key: "name/alice", Value: []byte("u1")}})
	tr.must("PutAllIfAllUnchanged", err)
	tr.check("PutAllIfAllUnchanged(both hold)", applied, true)
	applied, err = multi.PutAllIfAllUnchanged(ctx, guards, []kvstore.KV{{Key: "name/alice", Value: []byte("u2")}})
	tr.must("PutAllIfAllUnchanged", err)
	tr.check("PutAllIfAllUnchanged(name taken)", applied, false)
	tr.check("name/alice", value(ctx, t, p, "name/alice"), `"u1"`)

	// A fence written by a plain Put — go-qrdb#484's tenant tombstone — must
	// stop a create guarded on its absence.
	tr.must("Put(tombstone)", p.kv.Put(ctx, "tomb/t1", []byte("retired")))
	applied, err = multi.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "tenant/t1"}, {Key: "tomb/t1"}},
		[]kvstore.KV{{Key: "tenant/t1", Value: []byte("live")}})
	tr.must("PutAllIfAllUnchanged(fenced)", err)
	tr.check("create against a tombstone", applied, false)
	tr.check("tenant/t1", value(ctx, t, p, "tenant/t1"), "<absent>")

	// Empty is not absent, in both directions.
	applied, err = multi.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "void", Value: []byte{}}}, nil)
	tr.must("PutAllIfAllUnchanged(empty guard)", err)
	tr.check("empty-value guard on an absent key", applied, false)
	tr.must("Put(empty)", p.kv.Put(ctx, "void", []byte{}))
	applied, err = multi.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "void", Value: []byte{}}}, nil)
	tr.must("PutAllIfAllUnchanged(empty guard)", err)
	tr.check("empty-value guard on an empty value", applied, true)
	applied, err = multi.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "void"}}, nil)
	tr.must("PutAllIfAllUnchanged(absent guard)", err)
	tr.check("absent guard on an empty value", applied, false)

	// --- raw transactions: both branches, deletes, version compares ---
	resp, err := p.tx.Execute(ctx,
		[]service.Compare{{Key: []byte("owner"), Target: service.CompareValue, Result: service.CompareEqual, Value: []byte("nope")}},
		[]service.Op{{Type: service.OpPut, Key: []byte("txn/success"), Value: []byte("x")}},
		[]service.Op{{Type: service.OpPut, Key: []byte("txn/failure"), Value: []byte("y")}, {Type: service.OpDelete, Key: []byte("idx/1")}})
	tr.must("Transaction(failure branch)", err)
	tr.check("txn(compare fails).Succeeded", resp.Succeeded, false)
	tr.check("txn/success", value(ctx, t, p, "txn/success"), "<absent>")
	tr.check("txn/failure", value(ctx, t, p, "txn/failure"), `"y"`)
	tr.check("idx/1 after failure-branch delete", value(ctx, t, p, "idx/1"), "<absent>")

	resp, err = p.tx.Execute(ctx,
		[]service.Compare{
			{Key: []byte("owner"), Target: service.CompareVersion, Result: service.CompareGreater, Version: 0},
			{Key: []byte("owner"), Target: service.CompareValue, Result: service.CompareLess, Value: []byte("B")},
		},
		[]service.Op{{Type: service.OpPut, Key: []byte("txn/success"), Value: []byte("x")}}, nil)
	tr.must("Transaction(success branch)", err)
	tr.check("txn(version>0 && value<B).Succeeded", resp.Succeeded, true)
	tr.check("txn/success", value(ctx, t, p, "txn/success"), `"x"`)

	// --- service batch: the MVCC opcodes the service layer emits ---
	_, err = p.batch.Execute(ctx, []service.Op{
		{Type: service.OpPut, Key: []byte("batch/a"), Value: []byte("1")},
		{Type: service.OpPut, Key: []byte("batch/b"), Value: []byte("2")},
		{Type: service.OpDelete, Key: []byte("rec/1")},
	})
	tr.must("BatchService.Execute", err)
	tr.check("batch/a", value(ctx, t, p, "batch/a"), `"1"`)
	tr.check("batch/b", value(ctx, t, p, "batch/b"), `"2"`)
	tr.check("rec/1 after batch delete", value(ctx, t, p, "rec/1"), "<absent>")

	// --- an empty key is refused before it is proposed ---
	_, err = p.tx.Execute(ctx,
		[]service.Compare{{Target: service.CompareVersion, Result: service.CompareEqual}},
		[]service.Op{{Type: service.OpPut, Key: []byte("txn/empty"), Value: []byte("x")}}, nil)
	var invalidKey *service.InvalidKeyError
	tr.check("txn with an empty compare key refused", errors.As(err, &invalidKey), true)
	_, err = p.batch.Execute(ctx, []service.Op{{Type: service.OpPut, Value: []byte("x")}})
	tr.check("batch with an empty key refused", errors.As(err, &invalidKey), true)
	tr.check("txn/empty", value(ctx, t, p, "txn/empty"), "<absent>")
}

// assertRawEmptyKeyRefused proposes an empty-key transaction and batch
// DIRECTLY, past the service layer's early refusal — as a proposer on an older
// release, or any other client of the log, can — and requires the state
// machine to refuse them as an application result and keep applying. Before
// go-qrdb#136's review fix this reached storage, and on disk and fast storage
// the error halted Apply on every replica.
func assertRawEmptyKeyRefused(ctx context.Context, t *testing.T, p planes) {
	t.Helper()
	compare := func(key string) []byte {
		b, err := protocol.EncodeCompare([]byte(key), byte(service.CompareVersion), byte(service.CompareEqual), nil, 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	op := func(key string) []byte {
		b, err := protocol.EncodeOp(protocol.OpMVCCPut, []byte(key), []byte("x"), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	txn, err := protocol.EncodeTransaction([][]byte{compare("")}, [][]byte{op("raw/txn")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := protocol.EncodeBatch([][]byte{op("raw/batch"), op("")})
	if err != nil {
		t.Fatal(err)
	}
	for name, cmd := range map[string][]byte{"transaction": txn, "batch": batch} {
		res, err := p.host.SyncPropose(ctx, 0, cmd)
		if err != nil {
			t.Fatalf("raw empty-key %s halted the state machine: %v", name, err)
		}
		if _, refused := protocol.DecodeAppError(res.Data); !refused {
			t.Fatalf("raw empty-key %s was not refused: %+v", name, res)
		}
	}
	for _, k := range []string{"raw/txn", "raw/batch"} {
		if got := value(ctx, t, p, k); got != "<absent>" {
			t.Errorf("a refused empty-key command wrote %s=%s", k, got)
		}
	}
	if err := p.kv.Put(ctx, "raw/after", []byte("ok")); err != nil {
		t.Fatalf("a plain Put failed after the refusal: %v", err)
	}
}

// stateKeys is every key the battery can leave behind, read back after a
// restore.
var stateKeys = []string{
	"owner", "nothing", "release", "rec/1", "rec/2", "idx/1", "name/alice", "tomb/t1", "tenant/t1",
	"void", "txn/success", "txn/failure", "batch/a", "batch/b", "fresh",
}

func TestGuardedPlaneConformsOnEverySMType(t *testing.T) {
	ctx := context.Background()
	var reference []string

	for _, c := range smCells(t) {
		t.Run(c.name, func(t *testing.T) {
			tr := &transcript{t: t, cell: c.name}
			machine := c.open(t, 1)
			p := planesOver(t, machine)

			battery(ctx, tr, p)
			if c.registry {
				assertRawEmptyKeyRefused(ctx, t, p)
			}

			// A create_revision compare: accepted where revisions exist,
			// refused — typed, and writing nothing — everywhere else.
			_, err := p.tx.Execute(ctx,
				[]service.Compare{{Key: []byte("fresh"), Target: service.CompareCreateRevision, Result: service.CompareEqual}},
				[]service.Op{{Type: service.OpPut, Key: []byte("fresh"), Value: []byte("first")}}, nil)
			var refused *service.CapabilityUnsupportedError
			switch {
			case c.revisions && err != nil:
				t.Errorf("create_revision compare on an MVCC state machine: %v", err)
			case !c.revisions && !errors.As(err, &refused):
				t.Errorf("create_revision compare = %v; want the typed refusal every state "+
					"machine without revisions returns", err)
			case !c.revisions:
				if !strings.Contains(refused.Message, "create_revision/mod_revision") {
					t.Errorf("revision refusal reads %q; want pebble's text", refused.Message)
				}
				if got := value(ctx, t, p, "fresh"); got != "<absent>" {
					t.Errorf("a refused revision compare wrote fresh=%s", got)
				}
			}

			// --- snapshot and restore into a fresh instance ---
			before := make(map[string]string, len(stateKeys))
			for _, k := range stateKeys {
				before[k] = value(ctx, t, p, k)
			}
			snap := snapshotOf(t, machine)

			restored := c.open(t, 2)
			restoreInto(t, restored, snap)
			rp := planesOver(t, restored)
			for _, k := range stateKeys {
				if got := value(ctx, t, rp, k); got != before[k] {
					t.Errorf("after restore %s = %s; the snapshot held %s", k, got, before[k])
				}
			}
			// The restored state decides guards as the original did.
			applied, err := rp.guarded.(kvstore.MultiGuardedBatchKVStore).PutAllIfAllUnchanged(ctx,
				[]kvstore.Guard{{Key: "owner", Value: []byte("A")}, {Key: "name/alice"}}, nil)
			tr.must("guard after restore", err)
			tr.check("restored: guard on a taken name", applied, false)

			if c.name == "pebble" {
				reference = tr.lines
				return
			}
			if reference == nil {
				t.Fatal("the pebble reference cell must run first")
			}
			if !equalStrings(tr.lines, reference) {
				t.Errorf("%s answered differently from the default state machine:\n%s",
					c.name, transcriptDiff(reference, tr.lines))
			}
		})
	}
}

func transcriptDiff(want, got []string) string {
	var b strings.Builder
	for i := 0; i < len(want) || i < len(got); i++ {
		var w, g string
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if w != g {
			fmt.Fprintf(&b, "  pebble: %s\n  this:   %s\n", w, g)
		}
	}
	return b.String()
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
