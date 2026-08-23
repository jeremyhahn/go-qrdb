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

// IndexConfig configures a secondary index at creation time. The zero value
// describes the server's default index: non-unique, non-sparse,
// string-ordered and case-SENSITIVE.
type IndexConfig struct {
	// Unique enforces a one-to-one mapping between indexed values and keys.
	Unique bool `json:"unique"`

	// Sparse skips keys where the indexed field is absent.
	Sparse bool `json:"sparse"`

	// Numeric encodes field values as fixed-width 8-byte big-endian integers
	// so byte ordering matches numeric ordering.
	Numeric bool `json:"numeric"`

	// CaseInsensitive folds string field values on write, backfill and lookup,
	// so "Alpha" and "alpha" are the same term.
	//
	// Named for the non-default so the zero value is the default and so the
	// field agrees with the wire encoding underneath it: both mark the
	// POSITIVE claim, and an absent value therefore means case-sensitive --
	// what every request written before this field existed meant
	// (go-qrdb#34).
	CaseInsensitive bool `json:"case_insensitive"`
}

// IndexQueryResponse is the response from an index query operation.
type IndexQueryResponse struct {
	Keys []string `json:"keys"`
}

// IndexScanEntry maps an index value to its matching entity keys.
type IndexScanEntry struct {
	Value string   `json:"value"`
	Keys  []string `json:"keys"`
}

// IndexScanResponse is the response from an index scan operation.
type IndexScanResponse struct {
	Entries []IndexScanEntry `json:"entries"`
}

// IndexListResponse is the response from listing all indexes.
type IndexListResponse struct {
	Names []string `json:"names"`
}
