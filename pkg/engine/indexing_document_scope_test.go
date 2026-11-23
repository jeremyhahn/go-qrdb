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
	"testing"

	gocodeccbor "github.com/jeremyhahn/go-codec/pkg/cbor"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// The tests in this file are replay regressions. A committed put of a value
// that is not a JSON object, under a key a collection ("__col:") index
// covers, applies as it always has: the value is stored and not indexed.
// Apply semantics of an existing Raft command never change, so an old log
// replays and a mixed-version cluster converges; collections refuse such a
// write before it is proposed (pkg/service).

// cborDocument is a collection document in a non-JSON storage codec: valid
// CBOR, unreadable to the JSON index extractor.
func cborDocument(t *testing.T) string {
	t.Helper()
	b, err := gocodeccbor.New[map[string]string]().Marshal(map[string]string{"email": "a@example.com"})
	if err != nil {
		t.Fatalf("cbor Marshal: %v", err)
	}
	return string(b)
}

func uniqueIndexCmd(name, keyPrefix string) []byte {
	return mustEncode(protocol.EncodeIndexCreateOpts(name, "email", protocol.IndexCreateOptions{
		Unique:    true,
		KeyPrefix: keyPrefix,
	}))
}

func assertDocWriteAdmitted(t *testing.T, res sm.Result) {
	t.Helper()
	if msg, ok := protocol.DecodeAppError(res.Data); ok {
		t.Fatalf("write was refused: %s", msg)
	}
}

func TestCheckUniqueViolation_ReplayIndexedNonJSONDocumentAdmitted(t *testing.T) {
	const name = "t/db/db.users.email"
	indexes := map[string]index.Index{name: nil}
	scopes := map[string][]byte{name: ComposeIndexScope(name, "__col:db:users/")}
	key := []byte("t/db/__col:db:users/1")

	if err := CheckUniqueViolation(nil, indexes, map[string]string{name: "email"}, scopes, key, []byte(cborDocument(t))); err != nil {
		t.Fatalf("CheckUniqueViolation = %v, want nil (not a JSON object, nothing to index)", err)
	}
	if err := CheckUniqueViolationsBatch(nil, indexes, map[string]string{name: "email"}, scopes,
		[]indexedPut{{Key: key, Value: []byte(cborDocument(t))}}, nil); err != nil {
		t.Fatalf("CheckUniqueViolationsBatch = %v, want nil", err)
	}
}

func TestMVCCEngine_ReplayIndexedNonJSONDocumentApplies(t *testing.T) {
	eng := newScopedEngine(t)
	const idxName = "db.users.email"
	applyOne(t, eng, 1, uniqueIndexCmd(idxName, "__col:db:users/"))

	assertDocWriteAdmitted(t, applyMVCCPut(t, eng, 2, "__col:db:users/1", cborDocument(t)))

	kv, err := eng.store.Get([]byte("__col:db:users/1"))
	if err != nil || kv == nil {
		t.Fatalf("committed document not stored: kv=%v err=%v", kv, err)
	}
	if keys := indexedKeys(t, eng, idxName); len(keys) != 0 {
		t.Fatalf("index entries for a non-JSON document: %v", keys)
	}
}

func TestMVCCEngine_DocumentScopeOtherScopesAdmitUndecodable(t *testing.T) {
	cases := []struct {
		label, idxName, prefix, key string
	}{
		{"unscoped", "users.email", "", "__col:db:users/1"},
		{"non-collection prefix", "app.users.email", "app/users/", "app/users/1"},
	}
	for i, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			eng := newScopedEngine(t)
			applyOne(t, eng, 1, uniqueIndexCmd(c.idxName, c.prefix))
			assertDocWriteAdmitted(t, applyMVCCPut(t, eng, 2, c.key, cborDocument(t)))
			kv, err := eng.store.Get([]byte(c.key))
			if err != nil {
				t.Fatalf("case %d Get: %v", i, err)
			}
			if kv == nil {
				t.Fatalf("case %d: admitted write did not land", i)
			}
		})
	}
}

func TestKVEngine_ReplayIndexedNonJSONDocumentApplies(t *testing.T) {
	eng := newScopedKVEngine(t)
	const idxName = "t/db/db.users.email"
	applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: uniqueIndexCmd(idxName, "__col:db:users/")}})

	key := "t/db/__col:db:users/1"
	res := applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, key, cborDocument(t))})
	assertDocWriteAdmitted(t, res[0])

	val, err := eng.stg.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val == nil {
		t.Fatal("committed document not stored")
	}
	if keys := kvIndexedKeys(t, eng, idxName); len(keys) != 0 {
		t.Fatalf("index entries for a non-JSON document: %v", keys)
	}
}

func TestKVEngine_DocumentScopeOtherScopesAdmitUndecodable(t *testing.T) {
	cases := []struct {
		label, idxName, prefix, key string
	}{
		{"unscoped", "users.email", "", "__col:db:users/1"},
		{"non-collection prefix", "app.users.email", "app/users/", "app/users/1"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			eng := newScopedKVEngine(t)
			applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: uniqueIndexCmd(c.idxName, c.prefix)}})
			res := applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, c.key, cborDocument(t))})
			assertDocWriteAdmitted(t, res[0])
			val, err := eng.stg.Get([]byte(c.key))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if val == nil {
				t.Fatal("admitted write did not land")
			}
		})
	}
}
