// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package credentials

import (
	"bytes"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// openSecretCopy's coordinated borrow must not reach an allocation of
// protected memory.
//
// This is the EXECUTED half of go-qrdb#74's callee trace for this site. The
// callback is secure.CopyOut's make + copy, and the destroy of the transient
// buffer is deliberately outside the borrow; if either moved inside, this
// would hang rather than fail without the guard.
func TestOpenSecretCopy_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	const material = "a credential worth protecting"
	sec, err := newSecretCopy(t.Context(), []byte(material))
	if err != nil {
		t.Fatalf("newSecretCopy: %v", err)
	}
	defer sec.Destroy()

	out, err := openSecretCopy(t.Context(), sec)
	if err != nil {
		t.Fatalf("openSecretCopy: %v", err)
	}
	if !bytes.Equal(out, []byte(material)) {
		t.Error("openSecretCopy returned different bytes than were sealed")
	}
}

// A destroyed secret must report rather than return an empty copy, and must do
// so without taking a nested borrow on the failure path either.
//
// The failure path is checked separately because it is where a Destroy is
// easiest to misplace: a cleanup added inside the borrow is invisible on the
// success path and deadlocks only when something has already gone wrong.
func TestOpenSecretCopy_DestroyedSecretDoesNotBorrowNested(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	sec, err := newSecretCopy(t.Context(), []byte("about to be destroyed"))
	if err != nil {
		t.Fatalf("newSecretCopy: %v", err)
	}
	sec.Destroy()

	out, err := openSecretCopy(t.Context(), sec)
	if err == nil {
		t.Fatal("openSecretCopy on a destroyed secret returned no error")
	}
	if out != nil {
		t.Errorf("openSecretCopy returned %d bytes from a destroyed secret", len(out))
	}
}
