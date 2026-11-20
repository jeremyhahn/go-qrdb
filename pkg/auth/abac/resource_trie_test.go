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

package abac

import (
	"testing"
)

func TestResourceTrie_ExactMatch(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/config/database", "rule1")
	trie.Insert("keys/config/cache", "rule2")
	trie.Insert("keys/users/profile", "rule3")

	tests := []struct {
		name      string
		resource  string
		wantFound bool
		wantValue interface{}
	}{
		{
			name:      "exact match 1",
			resource:  "keys/config/database",
			wantFound: true,
			wantValue: "rule1",
		},
		{
			name:      "no match",
			resource:  "keys/config/unknown",
			wantFound: false,
		},
		{
			name:      "partial match not found",
			resource:  "keys/config",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _, found := trie.Match(tt.resource)
			if found != tt.wantFound {
				t.Errorf("Match() found = %v, want %v", found, tt.wantFound)
			}
			if found && value != tt.wantValue {
				t.Errorf("Match() value = %v, want %v", value, tt.wantValue)
			}
		})
	}
}

func TestResourceTrie_SingleLevelWildcard(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/users/*/profile", "rule1")
	trie.Insert("keys/tenants/*/config", "rule2")

	tests := []struct {
		name      string
		resource  string
		wantFound bool
		wantValue interface{}
	}{
		{
			name:      "wildcard match alice",
			resource:  "keys/users/alice/profile",
			wantFound: true,
			wantValue: "rule1",
		},
		{
			name:      "wildcard no match extra level",
			resource:  "keys/users/alice/bob/profile",
			wantFound: false,
		},
		{
			name:      "wildcard no match missing level",
			resource:  "keys/users/profile",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _, found := trie.Match(tt.resource)
			if found != tt.wantFound {
				t.Errorf("Match() found = %v, want %v", found, tt.wantFound)
			}
			if found && value != tt.wantValue {
				t.Errorf("Match() value = %v, want %v", value, tt.wantValue)
			}
		})
	}
}

func TestResourceTrie_MultiLevelWildcard(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/tenant/acme/**", "rule1")
	trie.Insert("keys/shared/**", "rule2")

	tests := []struct {
		name      string
		resource  string
		wantFound bool
		wantValue interface{}
	}{
		{
			name:      "multi-level single segment",
			resource:  "keys/tenant/acme/config",
			wantFound: true,
			wantValue: "rule1",
		},
		{
			name:      "multi-level multiple segments",
			resource:  "keys/tenant/acme/users/alice/profile",
			wantFound: true,
			wantValue: "rule1",
		},
		{
			name:      "multi-level no match different tenant",
			resource:  "keys/tenant/other/config",
			wantFound: false,
		},
		{
			name:      "multi-level shared",
			resource:  "keys/shared/public/data",
			wantFound: true,
			wantValue: "rule2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _, found := trie.Match(tt.resource)
			if found != tt.wantFound {
				t.Errorf("Match() found = %v, want %v", found, tt.wantFound)
			}
			if found && value != tt.wantValue {
				t.Errorf("Match() value = %v, want %v", value, tt.wantValue)
			}
		})
	}
}

func TestResourceTrie_VariableSubstitution(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/tenant/${subject.tenant}/data", "rule1")
	trie.Insert("keys/users/${subject.id}/**", "rule2")

	tests := []struct {
		name      string
		resource  string
		wantFound bool
		wantValue interface{}
		wantVars  map[string]string
	}{
		{
			name:      "variable match tenant",
			resource:  "keys/tenant/acme/data",
			wantFound: true,
			wantValue: "rule1",
			wantVars:  map[string]string{"subject.tenant": "acme"},
		},
		{
			name:      "variable match user",
			resource:  "keys/users/alice/profile",
			wantFound: true,
			wantValue: "rule2",
			wantVars:  map[string]string{"subject.id": "alice"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, vars, found := trie.Match(tt.resource)
			if found != tt.wantFound {
				t.Errorf("Match() found = %v, want %v", found, tt.wantFound)
			}
			if found && value != tt.wantValue {
				t.Errorf("Match() value = %v, want %v", value, tt.wantValue)
			}
			if found {
				for k, wantV := range tt.wantVars {
					if gotV, ok := vars[k]; !ok || gotV != wantV {
						t.Errorf("Match() vars[%s] = %v, want %v", k, gotV, wantV)
					}
				}
			}
		})
	}
}

func TestResourceTrie_GlobPattern(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/reports/2024-*-final", "rule1")
	trie.Insert("keys/cache/session-*", "rule2")

	tests := []struct {
		name      string
		resource  string
		wantFound bool
		wantValue interface{}
	}{
		{
			name:      "glob match report",
			resource:  "keys/reports/2024-01-final",
			wantFound: true,
			wantValue: "rule1",
		},
		{
			name:      "glob no match wrong year",
			resource:  "keys/reports/2023-01-final",
			wantFound: false,
		},
		{
			name:      "glob match session",
			resource:  "keys/cache/session-abc123",
			wantFound: true,
			wantValue: "rule2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, _, found := trie.Match(tt.resource)
			if found != tt.wantFound {
				t.Errorf("Match() found = %v, want %v", found, tt.wantFound)
			}
			if found && value != tt.wantValue {
				t.Errorf("Match() value = %v, want %v", value, tt.wantValue)
			}
		})
	}
}

func TestResourceTrie_Delete(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/config/database", "rule1")
	trie.Insert("keys/config/cache", "rule2")

	// Verify both exist
	if _, _, found := trie.Match("keys/config/database"); !found {
		t.Error("Expected to find keys/config/database before delete")
	}
	if _, _, found := trie.Match("keys/config/cache"); !found {
		t.Error("Expected to find keys/config/cache before delete")
	}

	// Delete one
	if !trie.Delete("keys/config/database") {
		t.Error("Delete should return true for existing pattern")
	}

	// Verify deletion
	if _, _, found := trie.Match("keys/config/database"); found {
		t.Error("Should not find keys/config/database after delete")
	}
	if _, _, found := trie.Match("keys/config/cache"); !found {
		t.Error("Should still find keys/config/cache after deleting database")
	}

	// Delete non-existent
	if trie.Delete("keys/config/nonexistent") {
		t.Error("Delete should return false for non-existent pattern")
	}
}

func TestResourceTrie_Clear(t *testing.T) {
	trie := NewResourceTrie()

	trie.Insert("keys/config/database", "rule1")
	trie.Insert("keys/config/cache", "rule2")
	trie.Insert("keys/users/*/profile", "rule3")

	if size := trie.Size(); size != 3 {
		t.Errorf("Size before clear = %d, want 3", size)
	}

	trie.Clear()

	if size := trie.Size(); size != 0 {
		t.Errorf("Size after clear = %d, want 0", size)
	}

	if _, _, found := trie.Match("keys/config/database"); found {
		t.Error("Should not find any patterns after clear")
	}
}

func TestResourceTrie_Size(t *testing.T) {
	trie := NewResourceTrie()

	if size := trie.Size(); size != 0 {
		t.Errorf("Initial size = %d, want 0", size)
	}

	trie.Insert("keys/config/database", "rule1")
	if size := trie.Size(); size != 1 {
		t.Errorf("Size after 1 insert = %d, want 1", size)
	}

	trie.Insert("keys/config/cache", "rule2")
	if size := trie.Size(); size != 2 {
		t.Errorf("Size after 2 inserts = %d, want 2", size)
	}

	trie.Delete("keys/config/database")
	if size := trie.Size(); size != 1 {
		t.Errorf("Size after delete = %d, want 1", size)
	}
}

func BenchmarkResourceTrie_Insert(b *testing.B) {
	trie := NewResourceTrie()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trie.Insert("keys/tenant/acme/data", "rule1")
	}
}

func BenchmarkResourceTrie_Match_Exact(b *testing.B) {
	trie := NewResourceTrie()
	trie.Insert("keys/config/database", "rule1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trie.Match("keys/config/database")
	}
}

func BenchmarkResourceTrie_Match_Wildcard(b *testing.B) {
	trie := NewResourceTrie()
	trie.Insert("keys/users/*/profile", "rule1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trie.Match("keys/users/alice/profile")
	}
}

func BenchmarkResourceTrie_Match_MultiLevel(b *testing.B) {
	trie := NewResourceTrie()
	trie.Insert("keys/tenant/acme/**", "rule1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		trie.Match("keys/tenant/acme/users/alice/profile")
	}
}
