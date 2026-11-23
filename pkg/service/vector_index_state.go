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

package service

import (
	"strconv"

	vectorindex "github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// VectorIndexWarmingError reports that a namespace's in-memory ANN index is
// not yet ready to answer a similarity search: still replaying its mutation
// log or rebuilding from records after open, or discarded and rebuilding
// after an index error recovered rather than failing the committed entry
// (docs/vectors/engineering.md "Open"). It is retryable: a caller that waits
// and searches again gets an answer once the namespace finishes warming.
// Get, list, and metadata search do not return this -- they read the
// replicated store directly and work immediately, independent of the
// in-memory index's state.
type VectorIndexWarmingError struct {
	Namespace string
}

// Error returns a human-readable description.
func (e *VectorIndexWarmingError) Error() string {
	return "vector: namespace " + strconv.Quote(e.Namespace) + " index is warming, retry"
}

// IsRetryable reports that this condition resolves on its own; a caller may
// retry the same request without changing it.
func (e *VectorIndexWarmingError) IsRetryable() bool { return true }

// VectorSearchQuery is an engine.Lookup query for the k nearest neighbours
// of Query in Namespace's in-memory ANN index. It answers
// *vector.NamespaceNotFoundError when the namespace does not exist, and
// *VectorIndexWarmingError when the namespace exists but its index is not
// yet ready.
type VectorSearchQuery struct {
	Namespace string
	Query     []float32
	K         int
}

// VectorSearchHit is one ranked result of a VectorSearchQuery.
type VectorSearchHit struct {
	ID       uint64
	Distance float32
}

// VectorNamespaceQuery asks the engine for one vector namespace's record.
// The answer is a *VectorNamespaceAnswer, or an untyped nil when the
// namespace does not exist.
type VectorNamespaceQuery struct {
	Namespace string
}

// VectorNamespaceListQuery asks the engine for every vector namespace. The
// answer is a []VectorNamespaceAnswer in ascending name order.
type VectorNamespaceListQuery struct{}

// VectorGetQuery asks the engine for one vector. The answer is a
// *vectorindex.VectorRecord, or an untyped nil when the namespace or the
// vector does not exist.
type VectorGetQuery struct {
	Namespace string
	ID        uint64
}

// VectorScanQuery asks the engine for the vectors of one namespace. The
// answer is a []VectorScanItem in ascending ID order starting at FromID
// (inclusive); Limit <= 0 returns every vector.
type VectorScanQuery struct {
	Namespace string
	FromID    uint64
	Limit     int
}

// VectorNamespaceAnswer is one namespace as the engine stores it: its
// record and the number of vectors under it.
type VectorNamespaceAnswer struct {
	Namespace string
	Record    vectorindex.NamespaceRecord
	Count     uint64
}

// VectorScanItem is one vector of a VectorScanQuery answer.
type VectorScanItem struct {
	ID     uint64
	Record vectorindex.VectorRecord
}
