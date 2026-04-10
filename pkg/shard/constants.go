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

// Package shard provides a registry for tracking active shards on a node
// and resolving shard IDs to their state machine types based on reserved
// ID ranges.
package shard

// Type identifies the kind of state machine a shard runs.
type Type string

const (
	// TypeKV identifies a key-value state machine.
	TypeKV Type = "kv"

	// TypeVector identifies a vector search state machine.
	TypeVector Type = "vector"

	// TypeTimeSeries identifies a time-series state machine.
	TypeTimeSeries Type = "timeseries"

	// TypeSystem identifies a system-internal state machine.
	TypeSystem Type = "system"

	// TypeUser identifies a user-defined state machine.
	TypeUser Type = "user"
)

// Reserved shard ID constants for core engines.
const (
	// ShardKV is the reserved shard ID for the primary key-value engine.
	ShardKV uint64 = 1

	// ShardVector is the reserved shard ID for the vector search engine.
	ShardVector uint64 = 2

	// ShardTimeSeries is the reserved shard ID for the time-series engine.
	ShardTimeSeries uint64 = 3
)

// Reserved shard ID ranges.
const (
	// CoreEngineMin is the lower bound of the core engine shard ID range.
	CoreEngineMin uint64 = 1

	// CoreEngineMax is the upper bound of the core engine shard ID range.
	CoreEngineMax uint64 = 99

	// SystemMin is the lower bound of the system shard ID range.
	SystemMin uint64 = 100

	// SystemMax is the upper bound of the system shard ID range.
	SystemMax uint64 = 199

	// InfraMin is the lower bound of the infrastructure shard ID range.
	InfraMin uint64 = 200

	// InfraMax is the upper bound of the infrastructure shard ID range.
	InfraMax uint64 = 299

	// UserMin is the lower bound of the user-defined shard ID range.
	UserMin uint64 = 1000
)
