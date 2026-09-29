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

package index

import (
	"errors"
	"testing"
)

func TestCollectionOwner(t *testing.T) {
	if got := CollectionOwner("db", "users", "ab"); got != "collection:db/users#ab" {
		t.Fatalf("CollectionOwner = %q", got)
	}
	// The dotted-name collision the index NAME cannot tell apart, the owner
	// can.
	if CollectionOwner("db", "users", "1") == CollectionOwner("db", "users.v2", "1") {
		t.Fatal("different collections must record different owners")
	}
}

func TestCheckCreateOwner(t *testing.T) {
	if err := CheckCreateOwner("n", "a", "a"); err != nil {
		t.Fatalf("same owner re-create must be idempotent: %v", err)
	}
	if err := CheckCreateOwner("n", "", ""); err != nil {
		t.Fatalf("explicit re-create must be idempotent: %v", err)
	}
	err := CheckCreateOwner("n", "", "collection:db/c#1")
	var own *OwnershipError
	if !errors.As(err, &own) || own.Name != "n" || own.Owner != "" || own.Requested != "collection:db/c#1" {
		t.Fatalf("a different owner must be refused: %v", err)
	}
}

func TestCheckDropOwner(t *testing.T) {
	if err := CheckDropOwner("n", "a", false, "b"); err != nil {
		t.Fatalf("an unconditional drop ignores ownership: %v", err)
	}
	if err := CheckDropOwner("n", "a", true, "a"); err != nil {
		t.Fatalf("the owner may drop: %v", err)
	}
	var own *OwnershipError
	if err := CheckDropOwner("n", "a", true, "b"); !errors.As(err, &own) {
		t.Fatalf("a stranger's owner-checked drop must be refused: %v", err)
	}
}

func TestOwnershipError_Error(t *testing.T) {
	err := &OwnershipError{Name: "n", Owner: "", Requested: "collection:db/c#1"}
	want := "index: n is owned by the explicit index API, not collection:db/c#1"
	if err.Error() != want {
		t.Fatalf("Error() = %q; want %q", err.Error(), want)
	}
	if OwnerLabel("x") != "x" {
		t.Fatal("a named owner labels as itself")
	}
}
