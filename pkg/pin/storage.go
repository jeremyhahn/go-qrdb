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

package pin

import (
	"context"
	"errors"
)

// Storage is the minimal key-value contract the SoftwareBackend uses to
// persist PIN records. It is intentionally narrow so any backing store —
// in-memory map for tests, qrdb's StorageBarrier, an HTTP-backed
// pluggable store — can be supplied without coupling pkg/pin to a
// concrete persistence layer.
//
// Implementations MUST return ErrNotFound (or an error that wraps it via
// errors.Is) when a key does not exist; SoftwareBackend treats that case
// as "fresh install" rather than a load failure.
type Storage interface {
	// Get retrieves the value stored under key. Implementations MUST
	// return ErrNotFound when the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)

	// Put writes value under key, overwriting any prior value.
	Put(ctx context.Context, key string, value []byte) error

	// Delete removes the record stored under key. Deleting an absent key
	// is NOT an error -- the record is already gone, which is the caller's
	// goal either way -- so an implementation must not return ErrNotFound
	// from Delete the way Get does.
	//
	// This exists for ProvisionRollback (go-qrdb#560 review D2):
	// SoftwareBackend.RollbackProvision un-sets its in-memory PIN state on
	// an aborted provisioning transaction, but before this method existed
	// it had no way to remove the PERSISTED record Put already wrote, so a
	// crash between the rollback and the next successful provisioning call
	// left a stale record behind -- one that would reload on the next
	// process start and require the aborted PIN, not the one the retry
	// actually used, to unlock it.
	Delete(ctx context.Context, key string) error
}

// ErrNotFound is returned by Storage implementations when a requested key
// is absent. Sentinel error so callers can errors.Is against it without
// importing a specific storage backend's package.
var ErrNotFound = errors.New("pin/storage: key not found")
