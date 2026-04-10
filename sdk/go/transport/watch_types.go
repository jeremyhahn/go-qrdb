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

// EventType identifies the type of a watch event.
type EventType uint8

const (
	// EventPut indicates a key was created or updated.
	EventPut EventType = iota

	// EventDelete indicates a key was deleted.
	EventDelete
)

// WatchEvent represents a single change to a key.
type WatchEvent struct {
	Type   EventType     `json:"type"`
	Kv     *MVCCKeyValue `json:"kv"`
	PrevKv *MVCCKeyValue `json:"prev_kv,omitempty"`
}

// WatchOptions configures a watch request.
type WatchOptions struct {
	StartRevision  int64 `json:"start_revision,omitempty"`
	PrevKV         bool  `json:"prev_kv,omitempty"`
	ProgressNotify bool  `json:"progress_notify,omitempty"`
}

// WatchResponse delivers watch events from the server.
type WatchResponse struct {
	WatchID      int64         `json:"watch_id"`
	Events       []*WatchEvent `json:"events"`
	Canceled     bool          `json:"canceled,omitempty"`
	CancelReason string        `json:"cancel_reason,omitempty"`
	Revision     int64         `json:"revision"`
}
