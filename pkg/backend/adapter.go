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

// Package backend provides a direct storage backend that implements the
// service.Host interface without Raft consensus. This enables QRDB to
// operate in memory, file, or direct PebbleDB modes where distributed
// consensus is not required.
package backend

import (
	"context"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// SMAdapter is the minimal interface that a state machine must implement
// to be used with LocalBackend. Both store.PebbleStateMachine and
// SimpleStateMachine satisfy this interface via structural typing.
type SMAdapter interface {
	// Update applies a batch of entries to the state machine.
	Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error

	// Lookup performs a read query against the state machine.
	Lookup(ctx context.Context, query interface{}) (interface{}, error)

	// Close releases all resources held by the state machine.
	Close(ctx context.Context) error
}
