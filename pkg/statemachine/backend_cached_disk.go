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

package statemachine

// CachedDiskBackend is a reserved placeholder for a cached disk backend
// that combines an in-memory LRU cache with synchronous disk I/O. It
// delegates to the CachedDiskStateMachine pattern from go-quicraft at
// the adapter level.
//
// This backend is not yet implemented. It will be wired through
// KVStateMachine and exposed via a CachedDiskAdapter in a future phase.
type CachedDiskBackend struct {
	// disk is the underlying disk backend for persistent storage.
	disk *DiskBackend
}

// NewCachedDiskBackend creates a new placeholder CachedDiskBackend.
// The actual caching layer will be implemented in a future phase.
func NewCachedDiskBackend(disk *DiskBackend) *CachedDiskBackend {
	return &CachedDiskBackend{disk: disk}
}
