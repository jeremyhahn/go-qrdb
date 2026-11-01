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

package vector

import (
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// ---------------------------------------------------------------------------
// PebbleDB vector storage types
// ---------------------------------------------------------------------------

// PebbleVectorStorage is a PebbleDB-backed vector storage engine.
type PebbleVectorStorage = vector.PebbleVectorStorage

// NewPebbleVectorStorage creates a new PebbleDB-backed vector storage.
var NewPebbleVectorStorage = vector.NewPebbleVectorStorage

// VectorStorageOption configures a PebbleVectorStorage instance.
type VectorStorageOption = vector.StorageOption

// VectorStorageWithEncryptor returns a VectorStorageOption that wraps the
// store's filesys.FS with barrier-driven AES-256-GCM encryption. A nil enc or
// an enc whose barrier is nil is a no-op, preserving plaintext operation.
func VectorStorageWithEncryptor(enc *atrest.Encryptor) VectorStorageOption {
	return vector.WithEncryptor(enc)
}

// VectorStorageConfig configures PebbleDB vector storage.
type VectorStorageConfig = vector.Config

// DefaultVectorStorageConfig returns a default configuration for vector storage.
var DefaultVectorStorageConfig = vector.DefaultConfig

// VectorValue holds a single vector with metadata.
type VectorValue = vector.VectorValue

// VectorNamespaceInfo holds metadata about a vector namespace.
type VectorNamespaceInfo = vector.NamespaceInfo

// VectorNamespaceStats holds statistics about a vector namespace.
type VectorNamespaceStats = vector.NamespaceStats

// VectorScanOptions configures vector scan operations.
type VectorScanOptions = vector.ScanOptions

// VectorMetadataFilter describes a metadata filter for vector queries.
type VectorMetadataFilter = vector.MetadataFilter

// VectorFilterOperator defines the comparison operator for metadata filters.
type VectorFilterOperator = vector.FilterOperator

// VectorFilterOperator constants.
const (
	VectorFilterEqual    = vector.FilterEqual
	VectorFilterNotEqual = vector.FilterNotEqual
	VectorFilterIn       = vector.FilterIn
	VectorFilterNotIn    = vector.FilterNotIn
)
