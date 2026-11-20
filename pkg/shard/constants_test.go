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
