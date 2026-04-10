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

// MVCCKeyValue is a versioned key-value pair with MVCC revision metadata.
type MVCCKeyValue struct {
	Key            []byte `json:"key"`
	Value          []byte `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// SortOrder specifies the sort direction for range results.
type SortOrder uint8

const (
	// SortNone returns results in natural key order.
	SortNone SortOrder = iota

	// SortAscend returns results in ascending order.
	SortAscend

	// SortDescend returns results in descending order.
	SortDescend
)

// SortTarget specifies which field to sort by.
type SortTarget uint8

const (
	// SortByKey sorts by the key.
	SortByKey SortTarget = iota

	// SortByVersion sorts by the version counter.
	SortByVersion

	// SortByCreateRevision sorts by the creation revision.
	SortByCreateRevision

	// SortByModRevision sorts by the modification revision.
	SortByModRevision

	// SortByValue sorts by the value.
	SortByValue
)

// RangeOptions configures a range query.
type RangeOptions struct {
	Limit      int64      `json:"limit,omitempty"`
	Revision   int64      `json:"revision,omitempty"`
	CountOnly  bool       `json:"count_only,omitempty"`
	KeysOnly   bool       `json:"keys_only,omitempty"`
	SortOrder  SortOrder  `json:"sort_order,omitempty"`
	SortTarget SortTarget `json:"sort_target,omitempty"`
}

// RangeResponse is the response from a range query.
type RangeResponse struct {
	Kvs   []*MVCCKeyValue `json:"kvs"`
	Count int64           `json:"count"`
}

// PutWithLeaseResponse is returned by PutWithLease.
type PutWithLeaseResponse struct {
	Kv *MVCCKeyValue `json:"kv"`
}

// CurrentRevisionResponse is returned by CurrentRevision.
type CurrentRevisionResponse struct {
	Revision int64 `json:"revision"`
}
