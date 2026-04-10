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

// coreEngineTypes maps well-known core engine shard IDs to their
// state machine types. This is consulted first by Resolve for O(1)
// dispatch of the three reserved engine IDs.
var coreEngineTypes = map[uint64]Type{
	ShardKV:         TypeKV,
	ShardVector:     TypeVector,
	ShardTimeSeries: TypeTimeSeries,
}

// Resolver maps shard IDs to their state machine type based on the
// reserved ID ranges.
type Resolver struct{}

// NewResolver creates a new Resolver.
func NewResolver() *Resolver {
	return &Resolver{}
}

// Resolve returns the Type for the given shard ID based on the reserved
// ID ranges. Core engine IDs (1-3) are mapped to their specific type;
// other IDs within 1-99 default to TypeKV.
func (r *Resolver) Resolve(shardID uint64) Type {
	if t, ok := coreEngineTypes[shardID]; ok {
		return t
	}
	if shardID >= CoreEngineMin && shardID <= CoreEngineMax {
		return TypeKV
	}
	if shardID >= SystemMin && shardID <= SystemMax {
		return TypeSystem
	}
	if shardID >= InfraMin && shardID <= InfraMax {
		return TypeSystem
	}
	return TypeUser
}
