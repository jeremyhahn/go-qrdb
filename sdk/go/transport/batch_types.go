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

// BatchRequest is the payload for executing a batch.
type BatchRequest struct {
	Ops []Op `json:"ops"`
}

// BatchResponse is the result of a batch execution.
type BatchResponse struct {
	Responses []OpResponse `json:"responses"`
}

// ImportOptions configures a batch import operation.
type ImportOptions struct {
	// Format specifies the file format: "csv", "json", "ndjson",
	// "yaml", "archive", or "auto". When "auto" or empty, the format
	// is inferred from Filename and the first 8 KB of data.
	Format string `json:"format"`

	// Filename is used for format auto-detection when Format is "auto"
	// or empty. It has no effect when Format is explicitly set.
	Filename string `json:"filename"`

	// KeyField is the field name to extract as the key.
	KeyField string `json:"key_field"`

	// ValueMode specifies how to extract the value: "field" uses a
	// specific field, "row" serializes the entire row as JSON.
	ValueMode string `json:"value_mode"`

	// ValueField is the field name for the value when ValueMode is
	// "field".
	ValueField string `json:"value_field,omitempty"`

	// SkipHeader indicates whether to skip the first row in CSV files.
	SkipHeader bool `json:"skip_header"`

	// Transactional enables all-or-nothing mode where any error causes
	// the entire import to be rolled back.
	Transactional bool `json:"transactional"`

	// Collection is an optional target collection name.
	Collection string `json:"collection,omitempty"`

	// Database is an optional target database name.
	Database string `json:"database,omitempty"`
}

// ImportResult contains statistics from a batch import operation.
type ImportResult struct {
	// Total is the total number of rows processed.
	Total int `json:"total"`

	// Succeeded is the number of rows successfully imported.
	Succeeded int `json:"succeeded"`

	// Failed is the number of rows that failed to import.
	Failed int `json:"failed"`

	// Errors contains details about failed rows.
	Errors []ImportError `json:"errors,omitempty"`
}

// ImportError describes a single row that failed during import.
type ImportError struct {
	// Row is the 1-based row number that failed.
	Row int `json:"row"`

	// Key is the extracted key for the failed row.
	Key string `json:"key"`

	// Message describes why the row failed.
	Message string `json:"message"`
}
