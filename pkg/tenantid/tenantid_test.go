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

package tenantid

import "testing"

func TestIsSystem(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"empty string resolves to system", "", true},
		{"literal system id matches", System, true},
		{"ordinary tenant id does not match", "acme-corp", false},
		{"capitalized lookalike does not match", "System", false},
		{"trailing-space lookalike does not match", "system ", false},
		{"suffixed lookalike does not match", "system-2", false},
		{"leading-space lookalike does not match", " system", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsSystem(tc.id)
			if got != tc.want {
				t.Fatalf("IsSystem(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// TestNormalize_FoldsAbsentTenantOnly pins the fold itself: "" — and only ""
// — becomes System. Everything else is returned byte-for-byte,
// because Normalize is a boundary fold, not a validator: a boundary that
// silently rewrote a real tenant id would be a cross-tenant bug of its own.
func TestNormalize_FoldsAbsentTenantOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absent tenant folds to system", "", System},
		{"system tenant is already canonical", System, System},
		{"real tenant passes through", "acme", "acme"},
		{"whitespace is a real value, not absence", " ", " "},
		{"a tenant named like system is not system", "system-2", "system-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalize_IsIdempotent states the property every boundary relies on:
// normalizing twice is normalizing once, so a value that crosses two folding
// boundaries (context binding, then identity binding) is not disturbed by the
// second.
func TestNormalize_IsIdempotent(t *testing.T) {
	for _, in := range []string{"", System, "acme"} {
		once := Normalize(in)
		if twice := Normalize(once); twice != once {
			t.Fatalf("Normalize is not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

// TestNormalize_EstablishesIsSystemPrecondition is the assertion #188
// asked for and that nothing made before: whatever Normalize returns,
// IsSystem never observes the empty string.
//
// IsSystem's doc comment argued its safety from a precondition — that
// normalized paths have already folded "" — and the precondition was
// documented and enforced nowhere. This ties the two together in code: the
// predicate's empty branch survives for unscoped-store callers, and the fold
// is what guarantees an authorization decision never reaches it.
func TestNormalize_EstablishesIsSystemPrecondition(t *testing.T) {
	for _, in := range []string{"", System, "acme"} {
		normalized := Normalize(in)
		if normalized == "" {
			t.Fatalf("Normalize(%q) returned the empty string; the precondition IsSystem documents is not enforced", in)
		}
		// The fold must not change the answer, only the representation.
		if IsSystem(normalized) != IsSystem(in) {
			t.Fatalf("Normalize(%q) changed IsSystem from %v to %v",
				in, IsSystem(in), IsSystem(normalized))
		}
	}
}
