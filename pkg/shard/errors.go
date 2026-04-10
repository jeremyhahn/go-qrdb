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

import "strconv"

// ShardAlreadyRegisteredError is returned when attempting to register a
// shard that is already present in the registry.
type ShardAlreadyRegisteredError struct {
	ShardID uint64
}

func (e *ShardAlreadyRegisteredError) Error() string {
	return "shard: already registered: " + strconv.FormatUint(e.ShardID, 10)
}

// ShardNotRegisteredError is returned when an operation references a
// shard that is not present in the registry.
type ShardNotRegisteredError struct {
	ShardID uint64
}

func (e *ShardNotRegisteredError) Error() string {
	return "shard: not registered: " + strconv.FormatUint(e.ShardID, 10)
}

// InvalidShardIDError is returned when a shard ID of zero is provided.
type InvalidShardIDError struct {
	ShardID uint64
}

func (e *InvalidShardIDError) Error() string {
	return "shard: invalid shard ID: " + strconv.FormatUint(e.ShardID, 10)
}
