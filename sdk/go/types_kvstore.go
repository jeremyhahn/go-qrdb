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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/kvstore"

// ---------------------------------------------------------------------------
// KVStore types (core interface is already in dao.go as KVStore)
// ---------------------------------------------------------------------------

// AtomicKVStore is a KVStore that additionally offers PutIfAbsent — a write
// whose outcome is defined under concurrency.
//
// It is discovered by type assertion, so a consumer that needs exactly-once
// semantics asserts for it at construction and fails closed:
//
//	atomic, ok := store.(sdk.AtomicKVStore)
//	if !ok {
//	    return sdk.ErrKVAtomicWriteUnsupported
//	}
//
// Without this alias that assertion could only be written against
// pkg/kvstore, which consumers may not import.
type AtomicKVStore = kvstore.AtomicKVStore

// NewMemoryKVStore returns an in-process KVStore that supports atomic
// conditional writes and maintains registered secondary indexes for real.
//
// It is the supported way to unit-test DAO code without booting a node.
// Consumers previously had to hand-write a twelve-method index-aware fake
// per repository, and a fake with subtly wrong index semantics produces
// tests that prove nothing. Single-process only: its atomicity is a mutex,
// not consensus.
var NewMemoryKVStore = kvstore.NewMemoryKVStore

// MemoryKVStore is the concrete type NewMemoryKVStore returns.
type MemoryKVStore = kvstore.MemoryKVStore

// ErrKVIndexUnsupported is returned by a store that cannot maintain or serve
// secondary indexes, from RegisterEntityIndexes and from every index query
// method. Test it with errors.Is.
//
// Such a store returns this INSTEAD of an empty result, because an empty
// result with a nil error is indistinguishable from a maintained index with
// no matches. A DAO built over such a store still answers correctly: pkg/dao
// detects the refusal at construction and plans full scans.
var ErrKVIndexUnsupported = kvstore.ErrIndexUnsupported

// ErrKVAtomicWriteUnsupported is returned by PutIfAbsent on a store that
// cannot perform a write whose outcome is defined under concurrency. A store
// reports this rather than degrading to Exists-then-Put, which would deliver
// the exact race the capability exists to eliminate while reporting success.
var ErrKVAtomicWriteUnsupported = kvstore.ErrAtomicWriteUnsupported

// EntityIndex defines a secondary index on a DAO entity field.
type EntityIndex = kvstore.EntityIndex

// EntityRegisterer allows entity types to register their secondary indexes.
type EntityRegisterer = kvstore.EntityRegisterer

// LookupFunc is a callback used for custom index lookups.
type LookupFunc = kvstore.LookupFunc

// ---------------------------------------------------------------------------
// KVStore error types
// ---------------------------------------------------------------------------

// KVSealedError is returned when an operation is attempted on a sealed KVStore.
type KVSealedError = kvstore.SealedError

// KVPutError is returned when a KVStore Put operation fails.
type KVPutError = kvstore.PutError

// KVGetError is returned when a KVStore Get operation fails.
type KVGetError = kvstore.GetError

// KVDeleteError is returned when a KVStore Delete operation fails.
type KVDeleteError = kvstore.DeleteError

// KVScanError is returned when a KVStore Scan operation fails.
type KVScanError = kvstore.ScanError

// KVListError is returned when a KVStore List operation fails.
type KVListError = kvstore.ListError

// KVExistsError is returned when a KVStore Exists operation fails.
type KVExistsError = kvstore.ExistsError

// KVIndexQueryError is returned when a KVStore index query fails.
type KVIndexQueryError = kvstore.IndexQueryError

// KVIndexScanError is returned when a KVStore index scan fails.
type KVIndexScanError = kvstore.IndexScanError

// KVIndexRegistrationError is returned when KVStore index registration fails.
type KVIndexRegistrationError = kvstore.IndexRegistrationError
