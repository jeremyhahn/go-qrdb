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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestPebbleStateMachine_IndexOwnership holds the third index-applying state
// machine to the ownership contract the engines keep (go-qrdb#426): a
// different owner's create is refused, not adopted; an owner-checked drop by a
// stranger removes nothing; the owner's own drop removes the index.
func TestPebbleStateMachine_IndexOwnership(t *testing.T) {
	s := openExtendedStore(t)
	const name = "mydb.users.email"
	const owner = "collection:mydb/users#1"

	create := func(o string) []byte {
		return mustEncode(protocol.EncodeIndexCreateOpts(name, "email", protocol.IndexCreateOptions{Owner: o}))
	}
	drop := func(opts protocol.IndexDropOptions) []byte {
		return mustEncode(protocol.EncodeIndexDropOpts(name, opts))
	}

	results := applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: create(owner)},
		{Index: 2, Cmd: create("")},
		{Index: 3, Cmd: create(owner)},
		{Index: 4, Cmd: drop(protocol.IndexDropOptions{CheckOwner: true, Owner: "collection:mydb/users#2"})},
	})
	if _, _, ok := protocol.DecodeIndexOwnershipError(results[0].Data); ok {
		t.Fatal("the first create was refused")
	}
	if n, o, ok := protocol.DecodeIndexOwnershipError(results[1].Data); !ok || n != name || o != owner {
		t.Fatalf("a different owner's create must be refused naming the owner; got %v %q %q", ok, n, o)
	}
	if _, _, ok := protocol.DecodeIndexOwnershipError(results[2].Data); ok {
		t.Fatal("the owner's re-create must stay idempotent")
	}
	if _, _, ok := protocol.DecodeIndexOwnershipError(results[3].Data); !ok {
		t.Fatal("a stranger's owner-checked drop must be refused")
	}
	if _, ok := s.indexes[name]; !ok {
		t.Fatal("the refused drop removed the index")
	}

	results = applyExtEntries(t, s, []sm.Entry{
		{Index: 5, Cmd: drop(protocol.IndexDropOptions{CheckOwner: true, Owner: owner})},
	})
	if _, _, ok := protocol.DecodeIndexOwnershipError(results[0].Data); ok {
		t.Fatal("the owner's drop was refused")
	}
	if _, ok := s.indexes[name]; ok {
		t.Fatal("the owner's drop must remove the index")
	}
	if _, ok := s.indexOwners[name]; ok {
		t.Fatal("the owner record must go with the index")
	}
}
