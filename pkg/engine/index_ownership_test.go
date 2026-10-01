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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Persisted index ownership on the apply path (go-qrdb#426).
//
// Both engines serve the transports, so both are held to the same contract:
// a create naming an index another owner holds is REFUSED as an application
// error rather than adopted, the same owner's re-create stays idempotent, and
// an owner-checked drop removes only its caller's index. None of it may fail
// the apply: every outcome is a deterministic consequence of committed input.
// ---------------------------------------------------------------------------

const ownedIdx = "mydb.users.email"

func ownedCreate(t *testing.T, idx uint64, owner string) sm.Entry {
	t.Helper()
	return indexCreateEntry(t, idx, ownedIdx, "email", protocol.IndexCreateOptions{Owner: owner})
}

func ownedDrop(t *testing.T, idx uint64, opts protocol.IndexDropOptions) sm.Entry {
	t.Helper()
	cmd, err := protocol.EncodeIndexDropOpts(ownedIdx, opts)
	if err != nil {
		t.Fatalf("EncodeIndexDropOpts: %v", err)
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// wantOwnershipRefusal asserts r is an ownership refusal naming owner.
func wantOwnershipRefusal(t *testing.T, r sm.Result, owner string) {
	t.Helper()
	name, got, ok := protocol.DecodeIndexOwnershipError(r.Data)
	if !ok {
		t.Fatalf("expected an index-ownership refusal, got %+v", r)
	}
	if name != ownedIdx || got != owner {
		t.Fatalf("refusal names (%q, %q); want (%q, %q)", name, got, ownedIdx, owner)
	}
}

// wantNoRefusal asserts r carries no application error.
func wantNoRefusal(t *testing.T, r sm.Result) {
	t.Helper()
	if msg, ok := isAppErr(r); ok {
		t.Fatalf("unexpected application error: %s", msg)
	}
}

// indexExists reports whether a query against name is answered rather than
// refused as an unknown index, on either engine.
func indexExists(eng Engine, name string) bool {
	_, err := eng.Lookup(context.Background(), index.IndexQueryRequest{IndexName: name, Value: []byte("x")})
	return err == nil
}

// ownershipEngines builds one of each engine that applies index commands.
func ownershipEngines(t *testing.T) map[string]Engine {
	t.Helper()
	mvcc, stg := newMVCCEngine(t)
	t.Cleanup(func() { _ = stg.Close() })
	t.Cleanup(func() { _ = mvcc.Close(context.Background()) })
	return map[string]Engine{
		"mvcc": mvcc,
		"kv":   newScopedKVEngine(t),
	}
}

func TestEngines_IndexCreate_RefusesAnotherOwner(t *testing.T) {
	for name, eng := range ownershipEngines(t) {
		t.Run(name, func(t *testing.T) {
			results := applyKVEntries(t, eng, []sm.Entry{
				ownedCreate(t, 1, ""),
				ownedCreate(t, 2, "collection:mydb/users#1"),
				ownedCreate(t, 3, ""),
			})
			wantNoRefusal(t, results[0])
			wantOwnershipRefusal(t, results[1], "")
			wantNoRefusal(t, results[2])
			if !indexExists(eng, ownedIdx) {
				t.Fatal("the refused create must leave the owner's index in place")
			}
		})
	}
}

func TestEngines_IndexDrop_OwnerChecked(t *testing.T) {
	const owner = "collection:mydb/users#1"
	for name, eng := range ownershipEngines(t) {
		t.Run(name, func(t *testing.T) {
			results := applyKVEntries(t, eng, []sm.Entry{
				ownedCreate(t, 1, owner),
				ownedDrop(t, 2, protocol.IndexDropOptions{CheckOwner: true, Owner: "collection:mydb/users#2"}),
			})
			wantOwnershipRefusal(t, results[1], owner)
			if !indexExists(eng, ownedIdx) {
				t.Fatal("an owner-checked drop by a stranger must remove nothing")
			}

			results = applyKVEntries(t, eng, []sm.Entry{
				ownedDrop(t, 3, protocol.IndexDropOptions{CheckOwner: true, Owner: owner}),
				ownedDrop(t, 4, protocol.IndexDropOptions{CheckOwner: true, Owner: owner}),
			})
			wantNoRefusal(t, results[0])
			wantNoRefusal(t, results[1])
			if indexExists(eng, ownedIdx) {
				t.Fatal("the owner's drop must remove the index")
			}
		})
	}
}

func TestEngines_IndexDrop_UnconditionalIgnoresOwner(t *testing.T) {
	for name, eng := range ownershipEngines(t) {
		t.Run(name, func(t *testing.T) {
			results := applyKVEntries(t, eng, []sm.Entry{
				ownedCreate(t, 1, "collection:mydb/users#1"),
				indexDropEntry(t, 2, ownedIdx),
				// Recreated by a different owner once the name is free.
				ownedCreate(t, 3, ""),
			})
			for i, r := range results {
				if _, ok := isAppErr(r); ok {
					t.Fatalf("entry %d refused", i)
				}
			}
			if !indexExists(eng, ownedIdx) {
				t.Fatal("the name must be free for a new owner after an unconditional drop")
			}
		})
	}
}
