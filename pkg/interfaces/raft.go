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

package interfaces

import "context"

// RaftNode defines the minimal interface for Raft consensus operations
// needed by service implementations. The concrete implementation is
// provided by the application layer (e.g., dragondb's RaftNode).
type RaftNode interface {
	// SyncPropose proposes data to the specified Raft shard and waits
	// for consensus. Returns the result and any error.
	SyncPropose(ctx context.Context, shardID uint64, data []byte) (interface{}, error)
}
