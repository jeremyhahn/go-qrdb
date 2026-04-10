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

// WatchEventType identifies whether an entity watch event is a put or delete.
type WatchEventType int

const (
	// WatchEventPut indicates the entity was created or updated.
	WatchEventPut WatchEventType = 0

	// WatchEventDelete indicates the entity was deleted.
	WatchEventDelete WatchEventType = 1
)

// WatchEntityRequest contains parameters for watching a specific entity.
type WatchEntityRequest struct {
	// EntityType categorizes the entity to watch.
	EntityType EntityType

	// ID is the entity's unique identifier within its type.
	ID string

	// ShardID targets a specific shard. 0 uses the default shard.
	ShardID uint64

	// StartRevision replays events from this revision onward.
	// 0 means start from the current revision.
	StartRevision int64
}

// WatchEntityTypeRequest contains parameters for watching all entities
// of a given type.
type WatchEntityTypeRequest struct {
	// EntityType categorizes the entities to watch.
	EntityType EntityType

	// ShardID targets a specific shard. 0 uses the default shard.
	ShardID uint64

	// StartRevision replays events from this revision onward.
	// 0 means start from the current revision.
	StartRevision int64
}

// WatchEntityEvent represents a change to an entity observed by a watcher.
type WatchEntityEvent struct {
	// EntityType categorizes the changed entity.
	EntityType EntityType

	// ID is the entity's unique identifier within its type.
	ID string

	// Data is the entity payload after the change. Nil for deletes.
	Data []byte

	// Revision is the MVCC revision at which the change occurred.
	Revision int64

	// Type indicates whether the entity was put or deleted.
	Type WatchEventType
}
