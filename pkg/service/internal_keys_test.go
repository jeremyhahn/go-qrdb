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

package service

import "testing"

func TestIsInternalKey_MatchesEveryReservedPrefix(t *testing.T) {
	internal := []string{
		"__sys:",
		"__sys:roles/admin",
		"__sys:users/root",
		"ts:",
		"ts:cpu/1700000000",
		"idx:metric:",
		"idx:metric:cpu",
		"idx:tag:",
		"idx:tag:host=web1",
		"retention:",
		"retention:cpu",
	}
	for _, key := range internal {
		if !IsInternalKey(key) {
			t.Errorf("IsInternalKey(%q) = false, want true", key)
		}
	}
}

// TestIsInternalKey_AdmitsUserKeysThatMerelyResembleReservedOnes keeps the
// guard from over-reaching. It matches a prefix, not a substring and not a
// near-miss: "ts" without the colon and "idx:metrics" are ordinary user keys,
// and refusing them would deny legitimate restores.
func TestIsInternalKey_AdmitsUserKeysThatMerelyResembleReservedOnes(t *testing.T) {
	userKeys := []string{
		"",
		"app/a",
		"_sys:x",
		"__sy:foo",
		"__sysx:foo",
		"ts",
		"tsx:foo",
		"t:foo",
		"idx:",
		"idx:other",
		"idx:metrics", // "idx:metric:" requires the trailing colon
		"retention",
		"my__sys:key",
		"prefixed/ts:cpu",
	}
	for _, key := range userKeys {
		if IsInternalKey(key) {
			t.Errorf("IsInternalKey(%q) = true, want false", key)
		}
	}
}

// TestInternalKeyPrefixes_ReturnsIndependentCopy proves a caller cannot widen
// the user keyspace by mutating the slice the guard hands out. pkg/engine and
// pkg/store both hold the result in a package-level var for the lifetime of
// the process, so a shared backing array would let either one silently
// redefine what is internal for all three.
func TestInternalKeyPrefixes_ReturnsIndependentCopy(t *testing.T) {
	first := InternalKeyPrefixes()
	if len(first) == 0 {
		t.Fatal("InternalKeyPrefixes returned nothing")
	}
	first[0] = "clobbered"

	if !IsInternalKey("__sys:roles/admin") {
		t.Fatal("mutating the returned slice disabled the system-key guard")
	}
	if second := InternalKeyPrefixes(); second[0] == "clobbered" {
		t.Fatal("InternalKeyPrefixes shares its backing array with the caller")
	}
}

func TestInternalKeyPrefixes_ContainsTheSystemPrefix(t *testing.T) {
	if InternalKeyPrefixSystem != "__sys:" {
		t.Fatalf("InternalKeyPrefixSystem = %q, want __sys:", InternalKeyPrefixSystem)
	}
	if systemKeyPrefix != InternalKeyPrefixSystem {
		t.Fatalf("systemKeyPrefix (%q) has drifted from InternalKeyPrefixSystem (%q)",
			systemKeyPrefix, InternalKeyPrefixSystem)
	}

	for _, p := range InternalKeyPrefixes() {
		if p == InternalKeyPrefixSystem {
			return
		}
	}
	t.Fatal("the system prefix is missing from the internal prefix set")
}
