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
	"strings"
	"testing"
)

func TestVectorIndexWarmingError_ErrorAndRetryable(t *testing.T) {
	err := &VectorIndexWarmingError{Namespace: "docs"}
	if !strings.Contains(err.Error(), "docs") {
		t.Fatalf("Error() = %q, want it to name the namespace", err.Error())
	}
	if !err.IsRetryable() {
		t.Fatal("IsRetryable() = false, want true: a warming index resolves on its own")
	}
}

func TestVectorSearchQueryAndHit_FieldsRoundTrip(t *testing.T) {
	q := VectorSearchQuery{Namespace: "docs", Query: []float32{1, 2}, K: 5}
	if q.Namespace != "docs" || len(q.Query) != 2 || q.K != 5 {
		t.Fatalf("VectorSearchQuery = %+v, fields did not round-trip", q)
	}
	h := VectorSearchHit{ID: 7, Distance: 0.5}
	if h.ID != 7 || h.Distance != 0.5 {
		t.Fatalf("VectorSearchHit = %+v, fields did not round-trip", h)
	}
}
