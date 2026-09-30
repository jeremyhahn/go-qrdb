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

package ceremony

import "testing"

// TestSOPINMatches covers the constant-time SO PIN comparison: equal PINs
// match, and a PIN that differs in content, in length, or by being a prefix
// does not.
func TestSOPINMatches(t *testing.T) {
	cases := []struct {
		presented, configured string
		want                  bool
	}{
		{"test-so-pin", "test-so-pin", true},
		{"test-so-pim", "test-so-pin", false},
		{"test-so", "test-so-pin", false},
		{"test-so-pin-and-more", "test-so-pin", false},
		{"", "test-so-pin", false},
	}
	for _, c := range cases {
		if got := soPINMatches(c.presented, c.configured); got != c.want {
			t.Errorf("soPINMatches(%q, %q) = %v, want %v", c.presented, c.configured, got, c.want)
		}
	}
}

// TestInstanceID: every CeremonyService gets its own identifier, and it does
// not change.
func TestInstanceID(t *testing.T) {
	a, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	if err != nil {
		t.Fatalf("NewCeremonyService: %v", err)
	}
	b, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	if err != nil {
		t.Fatalf("NewCeremonyService: %v", err)
	}
	first := a.InstanceID()
	if first == 0 || first == b.InstanceID() {
		t.Fatalf("instance ids %d and %d: want distinct and non-zero", first, b.InstanceID())
	}
	if a.InstanceID() != first {
		t.Fatal("InstanceID changed between calls")
	}
}
