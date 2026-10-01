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

package secret_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// SecretValue.Bytes must copy under a coordinated borrow and must not reach an
// allocation of protected memory while holding it.
//
// The EXECUTED half of go-qrdb#74's callee trace for this site: the callback is
// secure.CopyOut's make + copy, and Destroy of the transient buffer is deferred
// OUTSIDE the borrow. Without the guard a misplaced allocation here hangs every
// caller that reads a resolved secret, which is most of configuration loading.
func TestSecretValueBytes_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	sv := mustSecretValue(t, "raw://x", []byte("s3cr3t"), secret.StrategyLiteral)
	defer sv.Clear()

	if got := string(sv.Bytes()); got != "s3cr3t" {
		t.Errorf("Bytes = %q, want s3cr3t", got)
	}
	// Repeated, because each call decrypts afresh and therefore takes a fresh
	// borrow: a leak in the guard bookkeeping would only show on the second.
	if got := string(sv.Bytes()); got != "s3cr3t" {
		t.Errorf("second Bytes = %q, want s3cr3t", got)
	}
}

// SecretValue.String must hold the borrow across the conversion, wipe the
// intermediate, and take no nested borrow doing it.
func TestSecretValueString_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	sv := mustSecretValue(t, "raw://x", []byte("s3cr3t"), secret.StrategyCredential)
	defer sv.Clear()

	if got := sv.String(); got != "s3cr3t" {
		t.Errorf("String = %q, want s3cr3t", got)
	}
	if got := sv.String(); got != "s3cr3t" {
		t.Errorf("second String = %q, want s3cr3t", got)
	}
}

// The empty and cleared cases must answer nothing without borrowing at all.
//
// Checked because they are the branches that return before the borrow, and a
// later change that moved the Open above the nil check would make every empty
// SecretValue take a borrow it does not need.
func TestSecretValue_EmptyAndClearedTakeNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	empty := mustSecretValue(t, "raw://y", nil, secret.StrategyLiteral)
	if got := empty.Bytes(); got != nil {
		t.Errorf("Bytes on an empty value = %v, want nil", got)
	}
	if got := empty.String(); got != "" {
		t.Errorf("String on an empty value = %q, want empty", got)
	}

	cleared := mustSecretValue(t, "raw://z", []byte("gone"), secret.StrategyLiteral)
	cleared.Clear()
	if got := cleared.Bytes(); got != nil {
		t.Errorf("Bytes after Clear = %v, want nil", got)
	}
	if got := cleared.String(); got != "" {
		t.Errorf("String after Clear = %q, want empty", got)
	}
}
