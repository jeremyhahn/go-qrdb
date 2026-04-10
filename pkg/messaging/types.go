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

package messaging

import "time"

// OpType represents the type of operation in a delta payload.
type OpType string

const (
	// OpSet sets a field to a new value.
	OpSet OpType = "set"
	// OpDelete removes a field.
	OpDelete OpType = "delete"
	// OpAppend appends to an array or string field.
	OpAppend OpType = "append"
	// OpIncrement increments a numeric field.
	OpIncrement OpType = "increment"
)

// String returns the string representation of the operation type.
func (ot OpType) String() string {
	return string(ot)
}

// DeltaPayload represents changes to an entity for efficient delta updates.
type DeltaPayload struct {
	// EntityID is the unique identifier of the entity being modified.
	EntityID string `json:"entity_id,omitempty"`

	// EntityType describes the type of entity.
	EntityType string `json:"entity_type,omitempty"`

	// Version is the new version number after changes.
	Version uint64 `json:"version"`

	// PrevVersion is the version number before changes.
	PrevVersion uint64 `json:"prev_version"`

	// Timestamp is when the changes were made.
	Timestamp time.Time `json:"timestamp"`

	// Changes is the list of field modifications.
	Changes []FieldChange `json:"changes"`
}

// FieldChange represents a single field modification in a delta payload.
type FieldChange struct {
	// Path is the JSON path to the modified field (e.g., "user.address.city").
	Path string `json:"path"`

	// Operation is the type of modification.
	Operation OpType `json:"op"`

	// Value is the new value for the field.
	// This is nil for delete operations.
	Value interface{} `json:"value,omitempty"`

	// OldValue is the previous value of the field.
	// This is only populated when include_old_values is enabled.
	OldValue interface{} `json:"old_value,omitempty"`
}
