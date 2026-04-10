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
// mapRAGHTTPError
// ---------------------------------------------------------------------------

func TestMapRAGHTTPError_DocNotFound(t *testing.T) {
	err := &service.RAGDocumentNotFoundError{ID: "doc1", Namespace: "ns"}
	code, ok := mapRAGHTTPError(err)
	if !ok {
		t.Fatal("expected ok=true for RAGDocumentNotFoundError")
	}
	if code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", code)
	}
}

func TestMapRAGHTTPError_ServiceClosed(t *testing.T) {
	err := &service.RAGServiceClosedError{}
	code, ok := mapRAGHTTPError(err)
	if !ok {
		t.Fatal("expected ok=true for RAGServiceClosedError")
	}
	if code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", code)
	}
}

func TestMapRAGHTTPError_Unknown(t *testing.T) {
	_, ok := mapRAGHTTPError(&service.KeyNotFoundError{Key: "k"})
	if ok {
		t.Error("expected ok=false for unknown error")
	}
}
