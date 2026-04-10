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
