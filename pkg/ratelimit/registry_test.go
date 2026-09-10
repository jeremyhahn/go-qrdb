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

package ratelimit

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	policy := Policy{RatePerSecond: 5, Burst: 10}
	r.Register(permission.Permission("kv:put"), policy)

	got, ok := r.Lookup(permission.Permission("kv:put"))
	if !ok {
		t.Fatal("want the registered permission to be found")
	}
	if got != policy {
		t.Fatalf("want %+v, got %+v", policy, got)
	}
}

func TestRegistry_LookupUnregisteredIsUnlimited(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup(permission.Permission("kv:get")); ok {
		t.Fatal("want an unregistered permission to report ok=false (unlimited)")
	}
}

func TestRegistry_LookupOnNilRegistryIsUnlimited(t *testing.T) {
	var r *Registry
	if _, ok := r.Lookup(permission.Permission("kv:put")); ok {
		t.Fatal("want a nil registry to report ok=false for every permission")
	}
	if entries := r.Entries(); entries != nil {
		t.Fatalf("want a nil registry's Entries to be nil, got %v", entries)
	}
}

func TestRegistry_RegisterPanicsOnEmptyPermission(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want Register to panic on an empty permission")
		}
	}()
	NewRegistry().Register(permission.Permission(""), Policy{RatePerSecond: 1, Burst: 1})
}

func TestRegistry_RegisterPanicsOnInvalidPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
	}{
		{"zero rate", Policy{RatePerSecond: 0, Burst: 5}},
		{"negative rate", Policy{RatePerSecond: -1, Burst: 5}},
		{"zero burst", Policy{RatePerSecond: 5, Burst: 0}},
		{"negative burst", Policy{RatePerSecond: 5, Burst: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want Register to panic on an invalid policy")
				}
			}()
			NewRegistry().Register(permission.Permission("kv:put"), tc.policy)
		})
	}
}

func TestRegistry_RegisterPanicsOnDuplicate(t *testing.T) {
	r := NewRegistry()
	r.Register(permission.Permission("kv:put"), Policy{RatePerSecond: 1, Burst: 1})

	defer func() {
		if recover() == nil {
			t.Fatal("want a duplicate Register to panic")
		}
	}()
	r.Register(permission.Permission("kv:put"), Policy{RatePerSecond: 2, Burst: 2})
}

func TestRegistry_EntriesReturnsAnIndependentCopy(t *testing.T) {
	r := NewRegistry()
	r.Register(permission.Permission("kv:put"), Policy{RatePerSecond: 1, Burst: 1})

	entries := r.Entries()
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	entries[permission.Permission("kv:put")] = Policy{RatePerSecond: 99, Burst: 99}

	got, _ := r.Lookup(permission.Permission("kv:put"))
	if got.RatePerSecond == 99 {
		t.Fatal("want mutating the returned map to leave the registry's own state untouched")
	}
}

func TestRegistry_EntriesOnEmptyRegistry(t *testing.T) {
	r := NewRegistry()
	entries := r.Entries()
	if len(entries) != 0 {
		t.Fatalf("want no entries on an empty registry, got %v", entries)
	}
}
