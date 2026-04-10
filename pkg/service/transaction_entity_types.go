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

import "github.com/jeremyhahn/go-qrdb/pkg/consistency"

// EntityCompare is a single precondition in an entity-typed transaction.
// It checks a field of the entity's key-value against a target value
// using the specified comparison operator.
type EntityCompare struct {
	// EntityType categorizes the entity.
	EntityType EntityType

	// ID is the entity's unique identifier within its type.
	ID string

	// Target selects which field to compare.
	Target CompareTarget

	// Result is the comparison operator.
	Result CompareResult

	// Value is the comparison operand when Target is CompareValue.
	Value []byte
}

// TransactionEntityOp is a single operation within an entity-typed transaction.
type TransactionEntityOp struct {
	// Type is the kind of operation: "put" or "delete".
	Type string

	// EntityType categorizes the entity.
	EntityType EntityType

	// ID is the entity's unique identifier within its type.
	ID string

	// Data is the entity payload for put operations. Ignored for deletes.
	Data []byte
}

// TransactionEntityRequest contains parameters for an entity-typed transaction.
type TransactionEntityRequest struct {
	// Compares is the set of preconditions to evaluate.
	Compares []EntityCompare

	// Success is the operations to execute when all compares pass.
	Success []TransactionEntityOp

	// Failure is the operations to execute when any compare fails.
	Failure []TransactionEntityOp

	// Consistency controls the read/write consistency level.
	Consistency consistency.Level

	// ShardID targets a specific shard. 0 uses the default shard.
	ShardID uint64
}

// TransactionEntityResponse holds the result of an entity-typed transaction.
type TransactionEntityResponse struct {
	// Succeeded is true when all preconditions passed and the success
	// operations were executed.
	Succeeded bool

	// Responses contains the raw response data from each executed operation.
	Responses [][]byte
}
