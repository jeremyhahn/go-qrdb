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

import "time"

// EntityType is a string identifier for a category of entities (e.g., "user", "config").
type EntityType string

// WriteMode controls the durability guarantee for write operations.
type WriteMode uint8

const (
	// WriteSync commits through full Raft quorum consensus.
	WriteSync WriteMode = iota

	// WriteWALDurable writes to the local WAL only.
	WriteWALDurable

	// WriteBatched batches writes for throughput optimization.
	WriteBatched
)

// PutEntityRequest contains parameters for storing an entity.
type PutEntityRequest struct {
	EntityType  EntityType
	ID          string
	Data        []byte
	Consistency ConsistencyLevel
	WriteMode   WriteMode
	TTL         time.Duration
	ShardID     uint64
}

// PutEntityResponse is returned after a successful entity store.
type PutEntityResponse struct {
	Version   int64
	Revision  int64
	Timestamp int64
}

// GetEntityRequest contains parameters for retrieving an entity.
type GetEntityRequest struct {
	EntityType  EntityType
	ID          string
	Consistency ConsistencyLevel
	ShardID     uint64
	Revision    int64 // optional: read at specific revision (0 = latest)
}

// GetEntityResponse is returned with the retrieved entity data.
type GetEntityResponse struct {
	Data      []byte
	Version   int64
	Revision  int64
	Timestamp int64
}

// DeleteEntityRequest contains parameters for deleting an entity.
type DeleteEntityRequest struct {
	EntityType  EntityType
	ID          string
	Consistency ConsistencyLevel
	WriteMode   WriteMode
	ShardID     uint64
}

// DeleteEntityResponse is returned after a successful entity deletion.
type DeleteEntityResponse struct {
	Deleted  bool
	Revision int64
}

// ListEntitiesRequest contains parameters for listing entities.
type ListEntitiesRequest struct {
	EntityType  EntityType
	Consistency ConsistencyLevel
	PageSize    int32
	PageToken   string
	ShardID     uint64
	Prefix      string // optional prefix filter within entity type
}

// ListEntitiesResponse is returned with a page of entity keys.
type ListEntitiesResponse struct {
	Keys          []string
	NextPageToken string
	TotalCount    int64
}

// entityKey builds the storage key for an entity: "{entityType}/{id}".
func entityKey(entityType EntityType, id string) string {
	return string(entityType) + "/" + id
}

// entityPrefix returns the prefix for listing entities of a given type.
func entityPrefix(entityType EntityType) string {
	return string(entityType) + "/"
}
