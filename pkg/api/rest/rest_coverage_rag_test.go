//go:build qrdb_vector

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

package rest

import (
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// RAG errors through the one REST mapping
// ---------------------------------------------------------------------------
//
// These used to exercise a build-tagged mapRAGHTTPError hook. The RAG error
// types are untagged and classified by pkg/api/errclass like every other
// family, so the hook is gone and the RAG handlers' statuses come from
// mapHTTPError.

func TestMapHTTPError_RAGDocNotFound(t *testing.T) {
	err := &service.RAGDocumentNotFoundError{ID: "doc1", Namespace: "ns"}
	if code := mapHTTPError(err); code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", code)
	}
}

func TestMapHTTPError_RAGServiceClosed(t *testing.T) {
	if code := mapHTTPError(&service.RAGServiceClosedError{}); code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", code)
	}
}
