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

func TestNewResolver(t *testing.T) {
	r := NewResolver()
	if r == nil {
		t.Fatal("NewResolver() returned nil")
	}
}

func TestResolveKVShardID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(ShardKV)
	if got != TypeKV {
		t.Errorf("Resolve(%d) = %q, want %q", ShardKV, got, TypeKV)
	}
}

func TestResolveKVExplicitID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(1)
	if got != TypeKV {
		t.Errorf("Resolve(1) = %q, want %q", got, TypeKV)
	}
}

func TestResolveVectorShardID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(ShardVector)
	if got != TypeVector {
		t.Errorf("Resolve(%d) = %q, want %q", ShardVector, got, TypeVector)
	}
}

func TestResolveVectorExplicitID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(2)
	if got != TypeVector {
		t.Errorf("Resolve(2) = %q, want %q", got, TypeVector)
	}
}

func TestResolveTimeSeriesShardID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(ShardTimeSeries)
	if got != TypeTimeSeries {
		t.Errorf("Resolve(%d) = %q, want %q", ShardTimeSeries, got, TypeTimeSeries)
	}
}

func TestResolveTimeSeriesExplicitID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(3)
	if got != TypeTimeSeries {
		t.Errorf("Resolve(3) = %q, want %q", got, TypeTimeSeries)
	}
}

func TestResolveCoreEngineDefaultKV(t *testing.T) {
	r := NewResolver()
	// IDs 4-99 within core engine range default to TypeKV.
	got := r.Resolve(4)
	if got != TypeKV {
		t.Errorf("Resolve(4) = %q, want %q", got, TypeKV)
	}
}

func TestResolveCoreEngineUpperBound(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(99)
	if got != TypeKV {
		t.Errorf("Resolve(99) = %q, want %q", got, TypeKV)
	}
}

func TestResolveCoreEngineMidRange(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(50)
	if got != TypeKV {
		t.Errorf("Resolve(50) = %q, want %q", got, TypeKV)
	}
}

func TestResolveSystemMin(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(SystemMin)
	if got != TypeSystem {
		t.Errorf("Resolve(%d) = %q, want %q", SystemMin, got, TypeSystem)
	}
}

func TestResolveSystemMax(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(SystemMax)
	if got != TypeSystem {
		t.Errorf("Resolve(%d) = %q, want %q", SystemMax, got, TypeSystem)
	}
}

func TestResolveSystemMidRange(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(150)
	if got != TypeSystem {
		t.Errorf("Resolve(150) = %q, want %q", got, TypeSystem)
	}
}

func TestResolveInfraMin(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(InfraMin)
	if got != TypeSystem {
		t.Errorf("Resolve(%d) = %q, want %q", InfraMin, got, TypeSystem)
	}
}

func TestResolveInfraMax(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(InfraMax)
	if got != TypeSystem {
		t.Errorf("Resolve(%d) = %q, want %q", InfraMax, got, TypeSystem)
	}
}

func TestResolveInfraMidRange(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(250)
	if got != TypeSystem {
		t.Errorf("Resolve(250) = %q, want %q", got, TypeSystem)
	}
}

func TestResolveUserMin(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(UserMin)
	if got != TypeUser {
		t.Errorf("Resolve(%d) = %q, want %q", UserMin, got, TypeUser)
	}
}

func TestResolveUserLargeID(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(50000)
	if got != TypeUser {
		t.Errorf("Resolve(50000) = %q, want %q", got, TypeUser)
	}
}

func TestResolveGapRangeAsUser(t *testing.T) {
	// IDs 300-999 fall in the gap between Infra and User ranges and
	// resolve to TypeUser.
	r := NewResolver()
	got := r.Resolve(300)
	if got != TypeUser {
		t.Errorf("Resolve(300) = %q, want %q", got, TypeUser)
	}
}

func TestResolveGapRangeUpperBound(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(999)
	if got != TypeUser {
		t.Errorf("Resolve(999) = %q, want %q", got, TypeUser)
	}
}

func TestResolveZeroAsUser(t *testing.T) {
	// Zero is not in any named range so it falls through to TypeUser.
	r := NewResolver()
	got := r.Resolve(0)
	if got != TypeUser {
		t.Errorf("Resolve(0) = %q, want %q", got, TypeUser)
	}
}

func TestResolveMaxUint64(t *testing.T) {
	r := NewResolver()
	got := r.Resolve(^uint64(0))
	if got != TypeUser {
		t.Errorf("Resolve(maxUint64) = %q, want %q", got, TypeUser)
	}
}

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Resolve(tt.shardID); got != tt.want {
				t.Errorf("Resolve(%d) = %q, want %q", tt.shardID, got, tt.want)
			}
		})
	}
}
