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

package permission

import (
	"errors"
	"testing"
)

func TestParseScope_RoundTrip(t *testing.T) {
	for _, s := range []Scope{ScopeSelf, ScopeCross, ScopeTenantOnly} {
		got, err := ParseScope(s.String())
		if err != nil {
			t.Fatalf("ParseScope(%q): %v", s.String(), err)
		}
		if got != s {
			t.Fatalf("ParseScope(%q) = %v, want %v", s.String(), got, s)
		}
	}
}

func TestParseScope_UnknownFailsClosed(t *testing.T) {
	var invalid *InvalidScopeError
	got, err := ParseScope("totally-bogus")
	if !errors.As(err, &invalid) {
		t.Fatalf("ParseScope(bogus) err = %v, want *InvalidScopeError", err)
	}
	if got != ScopeSelf {
		t.Fatalf("ParseScope(bogus) = %v, want ScopeSelf (fail closed)", got)
	}
}

func TestScope_Valid(t *testing.T) {
	for _, s := range []Scope{ScopeSelf, ScopeCross, ScopeTenantOnly} {
		if !s.Valid() {
			t.Fatalf("Scope(%d).Valid() = false, want true", s)
		}
	}
	if Scope(99).Valid() {
		t.Fatal("Scope(99).Valid() = true, want false")
	}
}
