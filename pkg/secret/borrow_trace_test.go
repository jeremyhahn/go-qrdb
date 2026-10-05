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

// The empty and cleared cases must answer nothing without borrowing at all.
//
// Checked because they are the branches that return before the borrow, and a
// later change that moved the Open above the nil check would make every empty
// SecretValue take a borrow it does not need. This now covers WithBytes and
// Open, the two surviving ctx-taking readers; the context-less Bytes/String
// accessors this test used to cover are gone (go-qrdb no-compat-cleanup).
func TestSecretValue_EmptyAndClearedTakeNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	empty := mustSecretValue(t, "raw://y", nil, secret.StrategyLiteral)
	if err := empty.WithBytes(t.Context(), func(b []byte) error {
		if b != nil {
			t.Errorf("WithBytes on an empty value saw %v, want nil", b)
		}
		return nil
	}); err != nil {
		t.Errorf("WithBytes on an empty value: %v", err)
	}
	if buf, err := empty.Open(t.Context()); err != nil || buf != nil {
		t.Errorf("Open on an empty value = (%v, %v), want (nil, nil)", buf, err)
	}

	cleared := mustSecretValue(t, "raw://z", []byte("gone"), secret.StrategyLiteral)
	cleared.Clear()
	if err := cleared.WithBytes(t.Context(), func(b []byte) error {
		if b != nil {
			t.Errorf("WithBytes after Clear saw %v, want nil", b)
		}
		return nil
	}); err != nil {
		t.Errorf("WithBytes after Clear: %v", err)
	}
	if buf, err := cleared.Open(t.Context()); err != nil || buf != nil {
		t.Errorf("Open after Clear = (%v, %v), want (nil, nil)", buf, err)
	}
}
