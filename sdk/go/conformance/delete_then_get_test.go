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

package conformance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// deleteThenGetRounds is how many Put, Delete, Get rounds each cell runs. A
// read that could observe a delete late would have to do it in none of them.
const deleteThenGetRounds = 50

// hashedKeyLen is long enough that the objstore backend stores the key under
// a hashed object name rather than a direct one (pkg/storage/objstore,
// objectName): the shape of key go-qrdb#1163 reported as "key not found:
// <hash>".
const hashedKeyLen = 600

// deleteThenGetCells is the guarded-verb matrix plus a standalone objstore
// cell on every transport. The matrix walks the engines the guarded plane is
// conformance-tested on; the objstore backend is the one a content-addressed
// object store embeds, so it is walked here as well.
func deleteThenGetCells() []cell {
	cells := matrix()
	for _, tk := range transportKinds {
		cells = append(cells, cell{backend: database.BackendObjstore, mode: standalone, transport: tk, want: fullPassage})
	}
	return cells
}

// TestDeleteThenGetIsTypedNotFound is go-qrdb#1163's contract: once Delete
// has returned, Get reports the key as a *transport.KeyNotFoundError and
// Exists reports false, on every engine and every transport, for a short key
// and for one long enough to be stored under a hashed name.
func TestDeleteThenGetIsTypedNotFound(t *testing.T) {
	for _, c := range deleteThenGetCells() {
		t.Run(c.name(), func(t *testing.T) {
			cl := c.boot(t)
			shapes := map[string]string{
				"short":  "dtg/short/",
				"hashed": "dtg/hashed/" + strings.Repeat("h", hashedKeyLen) + "/",
			}
			for shape, prefix := range shapes {
				for round := range deleteThenGetRounds {
					// One bounded context per round, as callTimeout bounds a call: a
					// single context over every round's Put and Delete outlives
					// its deadline on a slow backend (objstore) without any call
					// being slow.
					ctx := boundedCtx(t)
					key := fmt.Sprintf("%s%d", prefix, round)
					if err := cl.kv.Put(ctx, key, []byte("v")); err != nil {
						t.Fatalf("%s round %d: Put: %v", shape, round, err)
					}
					if err := cl.kv.Delete(ctx, key); err != nil {
						t.Fatalf("%s round %d: Delete: %v", shape, round, err)
					}
					assertKeyAbsent(t, cl, key, fmt.Sprintf("%s round %d after Delete", shape, round))
				}
			}
		})
	}
}

// TestDeleteThenGetIsTypedNotFound_ReusedKey runs the rounds on ONE key, so
// every Get follows a Delete of a key that had a value a moment earlier and
// has a revision history behind it.
func TestDeleteThenGetIsTypedNotFound_ReusedKey(t *testing.T) {
	for _, c := range deleteThenGetCells() {
		t.Run(c.name(), func(t *testing.T) {
			cl := c.boot(t)
			const key = "dtg/reused"
			for round := range deleteThenGetRounds {
				// One bounded context per round, as callTimeout bounds a call: a
				// single context over every round's Put and Delete outlives
				// its deadline on a slow backend (objstore) without any call
				// being slow.
				ctx := boundedCtx(t)
				value := []byte(fmt.Sprintf("v%d", round))
				if err := cl.kv.Put(ctx, key, value); err != nil {
					t.Fatalf("round %d: Put: %v", round, err)
				}
				assertKeyPresent(t, cl, key, fmt.Sprintf("round %d after Put", round))
				if err := cl.kv.Delete(ctx, key); err != nil {
					t.Fatalf("round %d: Delete: %v", round, err)
				}
				assertKeyAbsent(t, cl, key, fmt.Sprintf("round %d after Delete", round))
			}
		})
	}
}
