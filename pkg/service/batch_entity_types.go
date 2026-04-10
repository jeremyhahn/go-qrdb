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

import (
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// BatchEntityOp is a single operation within an entity-typed batch.
type BatchEntityOp struct {
	// Type is the kind of operation: "put" or "delete".
	Type string

	// EntityType categorizes the entity (e.g., "user", "config").
	EntityType EntityType

	// ID is the entity's unique identifier within its type.
	ID string

	// Data is the entity payload for put operations. Ignored for deletes.
	Data []byte
}

// BatchEntityRequest contains parameters for an entity-typed batch execution.
type BatchEntityRequest struct {
	// Ops is the ordered list of entity operations to execute atomically.
	Ops []BatchEntityOp

	// Consistency controls the read/write consistency level.
	Consistency consistency.Level

	// WriteMode controls the durability guarantee.
	WriteMode writemode.Mode

	// ShardID targets a specific shard. 0 uses the default shard.
	ShardID uint64
}

// BatchEntityResponse holds the results of an entity-typed batch execution.
type BatchEntityResponse struct {
	// Results contains per-operation outcomes in the same order as the input.
	Results []BatchEntityOpResult
}

// BatchEntityOpResult is the outcome of a single operation within an
// entity-typed batch.
type BatchEntityOpResult struct {
	// Index is the zero-based position of this operation in the request.
	Index int

	// Error is nil on success or contains the operation-specific failure.
	Error error
}
