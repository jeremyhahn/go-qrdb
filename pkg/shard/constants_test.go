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

package shard

import "testing"

func TestTypeStringValues(t *testing.T) {
	tests := []struct {
		name string
		typ  Type
		want string
	}{
		{"TypeKV", TypeKV, "kv"},
		{"TypeVector", TypeVector, "vector"},
		{"TypeTimeSeries", TypeTimeSeries, "timeseries"},
		{"TypeSystem", TypeSystem, "system"},
		{"TypeUser", TypeUser, "user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(tt.typ); got != tt.want {
				t.Errorf("Type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTypeCanBeUsedAsMapKey(t *testing.T) {
	m := map[Type]int{
		TypeKV:         1,
		TypeVector:     2,
		TypeTimeSeries: 3,
		TypeSystem:     4,
		TypeUser:       5,
	}
	if len(m) != 5 {
		t.Errorf("expected 5 entries in type map, got %d", len(m))
	}
}

func TestShardIDConstants(t *testing.T) {
	tests := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"ShardKV", ShardKV, 1},
		{"ShardVector", ShardVector, 2},
		{"ShardTimeSeries", ShardTimeSeries, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestShardIDConstantsAreUnique(t *testing.T) {
	ids := []uint64{ShardKV, ShardVector, ShardTimeSeries}
	seen := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate shard ID constant: %d", id)
		}
		seen[id] = true
	}
}

func TestReservedRangeConstants(t *testing.T) {
	tests := []struct {
		name string
		got  uint64
		want uint64
	}{
		{"CoreEngineMin", CoreEngineMin, 1},
		{"CoreEngineMax", CoreEngineMax, 99},
		{"SystemMin", SystemMin, 100},
		{"SystemMax", SystemMax, 199},
		{"InfraMin", InfraMin, 200},
		{"InfraMax", InfraMax, 299},
		{"UserMin", UserMin, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestReservedRangesDoNotOverlap(t *testing.T) {
	if CoreEngineMax >= SystemMin {
		t.Errorf("CoreEngine range overlaps with System range: %d >= %d", CoreEngineMax, SystemMin)
	}
	if SystemMax >= InfraMin {
		t.Errorf("System range overlaps with Infra range: %d >= %d", SystemMax, InfraMin)
	}
	if InfraMax >= UserMin {
		t.Errorf("Infra range overlaps with User range: %d >= %d", InfraMax, UserMin)
	}
}

func TestCoreEngineIDsWithinRange(t *testing.T) {
	coreIDs := []uint64{ShardKV, ShardVector, ShardTimeSeries}
	for _, id := range coreIDs {
		if id < CoreEngineMin || id > CoreEngineMax {
			t.Errorf("core engine shard ID %d is outside range [%d, %d]", id, CoreEngineMin, CoreEngineMax)
		}
	}
}
