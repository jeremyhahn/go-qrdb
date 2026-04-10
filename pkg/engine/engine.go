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

// Package engine provides store engine implementations that sit between the
// storage backends (memory, file, pebble) and the state machine layer. Engines
// handle binary wire protocol decoding, typed API dispatch, and subsystem
// integration (indexes, leases, watches, time-series). State machines wrap
// engines for Raft distributed consensus; local mode uses engines directly
// via the LocalBackend.
package engine

import (
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
)

// Engine is the store engine interface that both KV and MVCC engines implement.
// It extends backend.SMAdapter with an engine type identifier. State machines
// wrap engines for Raft; local mode uses engines directly via LocalBackend.
type Engine interface {
	backend.SMAdapter

	// Type returns the engine type identifier ("kv" or "mvcc").
	Type() string
}

// EntityRegisterer allows entity types to register their index definitions
// with an engine. Engines that implement this interface create storage-backed
// indexes automatically and maintain them atomically during Put/Delete.
type EntityRegisterer interface {
	RegisterEntityType(entityType string, indexes []EntityIndex) error
}
