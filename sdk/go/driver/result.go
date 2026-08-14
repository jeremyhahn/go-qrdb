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

package driver

// result implements the database/sql driver.Result interface.
//
// For embedded connections both values come from the engine's OkResult and
// are exact. For network connections the wire protocol conveys neither, so
// unsupported is set and both accessors report that rather than returning a
// fabricated zero — a silent zero would read as "nothing was inserted", which
// is indistinguishable from a real result and worse than an error.
type result struct {
	lastInsertID int64
	rowsAffected int64
	unsupported  bool
}

// LastInsertId returns the auto-increment value the engine generated for an
// INSERT, or 0 when the statement generated none.
func (r *result) LastInsertId() (int64, error) {
	if r.unsupported {
		return 0, &ResultMetadataUnsupportedError{Field: "LastInsertId"}
	}
	return r.lastInsertID, nil
}

// RowsAffected returns the number of rows affected by an INSERT, UPDATE,
// or DELETE statement.
func (r *result) RowsAffected() (int64, error) {
	if r.unsupported {
		return 0, &ResultMetadataUnsupportedError{Field: "RowsAffected"}
	}
	return r.rowsAffected, nil
}
