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

func TestResolveTableDriven(t *testing.T) {
	r := NewResolver()
	tests := []struct {
		name    string
		shardID uint64
		want    Type
	}{
		{"KV engine", ShardKV, TypeKV},
		{"Vector engine", ShardVector, TypeVector},
		{"TimeSeries engine", ShardTimeSeries, TypeTimeSeries},
		{"Core engine lower default", 4, TypeKV},
		{"Core engine upper bound", 99, TypeKV},
		{"System lower bound", 100, TypeSystem},
		{"System upper bound", 199, TypeSystem},
		{"Infra lower bound", 200, TypeSystem},
		{"Infra upper bound", 299, TypeSystem},
		{"Gap 300", 300, TypeUser},
		{"Gap 500", 500, TypeUser},
		{"Gap 999", 999, TypeUser},
		{"User lower bound", 1000, TypeUser},
		{"User high", 100000, TypeUser},
		{"Zero as user", 0, TypeUser},
		{"Max uint64", ^uint64(0), TypeUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Resolve(tt.shardID); got != tt.want {
				t.Errorf("Resolve(%d) = %q, want %q", tt.shardID, got, tt.want)
			}
		})
	}
}
