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

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
)

// batchImportFormats lists the import file formats the batch import
// session accepts, in the order the desktop binding reports them.
var batchImportFormats = []string{"auto", "csv", "json", "ndjson", "yaml", "archive"}

// batchImportFormatsResponse is the body of GET /batch/import/formats.
type batchImportFormatsResponse struct {
	Formats []string `json:"formats"`
	Codecs  []string `json:"codecs"`
}

// handleBatchImportFormats reports the import formats and the compiled-in
// codecs, so a REST client (the browser UI) can offer the same choices the
// desktop binding's AvailableImportFormats and AvailableCodecs offer.
func (s *Server) handleBatchImportFormats(w http.ResponseWriter, _ *http.Request) {
	formats := make([]string, len(batchImportFormats))
	copy(formats, batchImportFormats)
	writeJSON(w, batchImportFormatsResponse{
		Formats: formats,
		Codecs:  codec.AvailableCodecs(),
	})
}
