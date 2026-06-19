// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pool

import (
	"fmt"
	"strconv"
)

// NilStorageError is returned by NewPebbleStateStore when the supplied
// storage is nil.
type NilStorageError struct{}

// Error implements the error interface.
func (e *NilStorageError) Error() string {
	return "qrdb/storage/pool: storage must not be nil"
}

// EmptyKeyError is returned when Get or Put is invoked with an empty
// logical key.
type EmptyKeyError struct{}

// Error implements the error interface.
func (e *EmptyKeyError) Error() string {
	return "qrdb/storage/pool: key must not be empty"
}

// GetError wraps an underlying Pebble Get failure with the logical
// key that triggered it.
type GetError struct {
	Key string
	Err error
}

// Error implements the error interface.
func (e *GetError) Error() string {
	return fmt.Sprintf("qrdb/storage/pool: get %q: %v", e.Key, e.Err)
}

// Unwrap returns the underlying error.
func (e *GetError) Unwrap() error { return e.Err }

// PutError wraps an underlying Pebble Set failure with the logical
// key that triggered it.
type PutError struct {
	Key string
	Err error
}

// Error implements the error interface.
func (e *PutError) Error() string {
	return fmt.Sprintf("qrdb/storage/pool: put %q: %v", e.Key, e.Err)
}

// Unwrap returns the underlying error.
func (e *PutError) Unwrap() error { return e.Err }

// VolumePoolStorageError is returned when the volume-pool state store (the
// dedicated Pebble that persists round-robin cursors) fails to open.
type VolumePoolStorageError struct {
	Err error
}

// Error implements the error interface.
func (e *VolumePoolStorageError) Error() string {
	return "qrdb/storage/pool: state store: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VolumePoolStorageError) Unwrap() error { return e.Err }

// VolumePoolRegisterError is returned when objstore's pool.Manager rejects a
// Register call (typically duplicate name or empty candidates).
type VolumePoolRegisterError struct {
	Name string
	Err  error
}

// Error implements the error interface.
func (e *VolumePoolRegisterError) Error() string {
	return "qrdb/storage/pool: register " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VolumePoolRegisterError) Unwrap() error { return e.Err }

// NilStateStoreError is returned by BuildPoolStrategy when the supplied
// state store is nil. The stateful strategies (round_robin) persist their
// cursor through it, so construction without one is a programming error,
// not a configuration problem.
type NilStateStoreError struct{}

// Error implements the error interface.
func (e *NilStateStoreError) Error() string {
	return "qrdb/storage/pool: state store must not be nil"
}

// NilReplicatedKVError is returned by NewBoundReplicatedStateStore and
// ReplicatedStateStore.Bind when the supplied replicated KV is nil.
type NilReplicatedKVError struct{}

// Error implements the error interface.
func (e *NilReplicatedKVError) Error() string {
	return "qrdb/storage/pool: replicated KV must not be nil"
}

// ReplicatedStateStoreUnboundError is returned by ReplicatedStateStore.Get
// and Put when no replicated KV has been bound yet. In clustered mode the
// replicated cursor store is constructed before the replicated system KV is
// available and bound once the data plane comes up; an operation issued
// before binding is a wiring ordering bug.
type ReplicatedStateStoreUnboundError struct{}

// Error implements the error interface.
func (e *ReplicatedStateStoreUnboundError) Error() string {
	return "qrdb/storage/pool: replicated state store has no bound replicated KV"
}

// VolumePoolStrategyError is returned when a volume-pool YAML config
// references an unknown strategy name.
type VolumePoolStrategyError struct {
	Strategy string
	Err      error
}

// Error implements the error interface.
func (e *VolumePoolStrategyError) Error() string {
	if e.Err != nil {
		return "qrdb/storage/pool: strategy " + strconv.Quote(e.Strategy) + ": " + e.Err.Error()
	}
	return "qrdb/storage/pool: strategy " + strconv.Quote(e.Strategy) + ": unsupported"
}

// Unwrap returns the underlying error.
func (e *VolumePoolStrategyError) Unwrap() error { return e.Err }

// VolumePoolPathError is returned when one of the configured local paths
// cannot be created or wrapped as an objstore Backend.
type VolumePoolPathError struct {
	Pool string
	Path string
	Err  error
}

// Error implements the error interface.
func (e *VolumePoolPathError) Error() string {
	return "qrdb/storage/pool: pool " + strconv.Quote(e.Pool) +
		" path " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VolumePoolPathError) Unwrap() error { return e.Err }
