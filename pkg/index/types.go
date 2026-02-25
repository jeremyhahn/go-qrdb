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

package index

// IndexConfig configures a secondary index.
type IndexConfig struct {
	// Unique enforces that each indexed value maps to at most one key.
	Unique bool

	// Sparse skips indexing keys where the indexed field is absent
	// or empty.
	Sparse bool
}

// IndexQueryRequest queries an index for entity IDs matching an exact value.
type IndexQueryRequest struct {
	IndexName string
	Value     []byte
}

// IndexScanRequest queries an index for entity IDs within a value range.
type IndexScanRequest struct {
	IndexName string
	Start     []byte
	End       []byte
}

// IndexListRequest lists all registered index names.
type IndexListRequest struct{}
