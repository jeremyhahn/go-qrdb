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

package transport

import "context"

// KVClient provides data-plane key-value operations.
type KVClient interface {
	// Put stores a key-value pair through Raft consensus.
	Put(ctx context.Context, key string, value []byte) error

	// Get retrieves a value by key with a linearizable read.
	// Returns KeyNotFoundError when the key does not exist.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes a key through Raft consensus.
	Delete(ctx context.Context, key string) error

	// List returns sorted keys matching the given prefix.
	// An empty prefix returns all keys.
	List(ctx context.Context, prefix string) ([]string, error)
}

// AdminClient provides control-plane cluster management operations.
type AdminClient interface {
	// Health returns the health status of the QRDB node.
	Health(ctx context.Context) (*HealthResponse, error)

	// Status returns detailed status information about the node
	// and all loaded shards.
	Status(ctx context.Context) (*StatusResponse, error)

	// Peers returns the peer membership for all loaded shards.
	Peers(ctx context.Context) (*PeersResponse, error)

	// AddMember adds a new replica to the specified shard.
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error

	// RemoveMember removes a replica from the specified shard.
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error

	// RequestSnapshot triggers an asynchronous snapshot for the
	// specified shard.
	RequestSnapshot(ctx context.Context, shardID uint64) error

	// Drain transitions the host into drain mode where new proposals
	// are rejected while in-flight operations complete.
	Drain(ctx context.Context) error
}

// Client combines transport lifecycle with typed service interfaces.
// All concrete transports (REST, gRPC, embedded) implement this
// composite interface.
type Client interface {
	Transport
	KVClient
	AdminClient
}
