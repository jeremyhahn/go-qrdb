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

package watch

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/messaging"
)

// ManagerEventType represents the type of a watch event in the Manager system.
type ManagerEventType int32

const (
	// PUT indicates a key was created or updated.
	PUT ManagerEventType = 0
	// DELETE indicates a key was deleted.
	DELETE ManagerEventType = 1
)

// String returns the string representation of the event type.
func (et ManagerEventType) String() string {
	switch et {
	case PUT:
		return "PUT"
	case DELETE:
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

// ManagerKeyValue represents a versioned key-value pair in the Manager watch system.
type ManagerKeyValue struct {
	// Key is the key in bytes.
	Key []byte
	// CreateRevision is the revision of last creation on this key.
	CreateRevision int64
	// ModRevision is the revision of last modification on this key.
	ModRevision int64
	// Version is the version of the key. A deletion resets the version to zero.
	Version int64
	// Value is the value held by the key, in bytes.
	Value []byte
	// Lease is the ID of the lease that attached to key. 0 for no lease.
	Lease int64
}

// ManagerEvent represents a simplified watch event for the Manager's Watch() API.
// This is a higher-level abstraction over ManagerWatchEvent that doesn't
// require gRPC stream infrastructure.
type ManagerEvent struct {
	Type     string // ManagerEventTypePut or ManagerEventTypeDelete
	Key      []byte // The key that changed
	Value    []byte // The new value (nil for delete)
	Revision int64  // The MVCC revision when this change occurred
	Version  int64  // The version number for this key (modification count)
}

// ManagerWatchEvent represents a single key change event in the Manager system.
// Reused via sync.Pool to reduce allocations.
type ManagerWatchEvent struct {
	Type     ManagerEventType        // PUT or DELETE
	Kv       *ManagerKeyValue        // Full value (when delta mode disabled)
	PrevKv   *ManagerKeyValue        // Optional previous value
	Delta    *messaging.DeltaPayload // Changed fields only (when delta mode enabled)
	Revision int64
}

// ManagerWatchResponse wraps one or more events for delivery to the client
// via the Manager's gRPC stream infrastructure.
type ManagerWatchResponse struct {
	WatchID  int64
	Events   []*ManagerWatchEvent
	Revision int64
}

// WatchFilters controls which events are delivered to a Manager watch.
type WatchFilters struct {
	NoPut    bool // Filter out PUT events
	NoDelete bool // Filter out DELETE events
}

// ManagerWatchStream is the interface for sending watch responses to clients.
// This abstraction allows testing without real gRPC streams.
type ManagerWatchStream interface {
	Send(*ManagerWatchResponse) error
	Context() context.Context
}

// Manager-specific event type string constants for the simplified Watch() API.
const (
	ManagerEventTypePut    = "PUT"
	ManagerEventTypeDelete = "DELETE"
)
