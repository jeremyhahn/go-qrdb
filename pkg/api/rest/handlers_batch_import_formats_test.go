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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
)

func TestHandleBatchImportFormats_ListsFormatsAndCodecs(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(&mockBatchService{}))

	req := httptest.NewRequest(http.MethodGet, "/batch/import/formats", nil)
	rr := httptest.NewRecorder()
	srv.handleBatchImportFormats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got batchImportFormatsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	wantFormats := []string{"auto", "csv", "json", "ndjson", "yaml", "archive"}
	if !reflect.DeepEqual(got.Formats, wantFormats) {
		t.Errorf("formats = %v, want %v", got.Formats, wantFormats)
	}
	if !reflect.DeepEqual(got.Codecs, codec.AvailableCodecs()) {
		t.Errorf("codecs = %v, want %v", got.Codecs, codec.AvailableCodecs())
	}
	if len(got.Codecs) == 0 {
		t.Error("codecs is empty; at least one codec is compiled in")
	}
}

func TestHandleBatchImportFormats_ResponseDoesNotAliasList(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(&mockBatchService{}))
	rr := httptest.NewRecorder()
	srv.handleBatchImportFormats(rr, httptest.NewRequest(http.MethodGet, "/batch/import/formats", nil))
	var got batchImportFormatsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if batchImportFormats[0] != "auto" {
		t.Fatalf("handler mutated the shared format list: %v", batchImportFormats)
	}
}

func TestBatchImportFormatsRoute_Registered(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithBatch(&mockBatchService{}))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/batch/import/formats", nil))
	if rr.Code == http.StatusNotFound || rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("GET /batch/import/formats not routed: %d %s", rr.Code, rr.Body.String())
	}
}
