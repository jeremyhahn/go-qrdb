// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package engine

// TranslationError is returned when a PostgreSQL query cannot be translated
// into go-mysql-server compatible SQL.
type TranslationError struct {
	Query  string
	Reason string
}

// Error returns a human-readable description of the translation failure.
func (e *TranslationError) Error() string {
	return "sql/engine: translation failed for query: " + e.Query + ": " + e.Reason
}

// QueryExecutionError is returned when a SQL query fails during execution.
// It wraps the underlying error from go-mysql-server.
type QueryExecutionError struct {
	Query string
	Err   error
}

// Error returns a human-readable description of the query execution failure.
func (e *QueryExecutionError) Error() string {
	return "sql/engine: query execution failed: " + e.Query + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *QueryExecutionError) Unwrap() error {
	return e.Err
}

// UnsupportedSyntaxError is returned when a PostgreSQL syntax feature has
// no equivalent translation in go-mysql-server.
type UnsupportedSyntaxError struct {
	Feature string
}

// Error returns a human-readable description of the unsupported feature.
func (e *UnsupportedSyntaxError) Error() string {
	return "sql/engine: unsupported PostgreSQL syntax: " + e.Feature
}

// UnsupportedColumnTypeError is returned when a PostgreSQL column type in a
// CREATE TABLE or ALTER TABLE column definition has no go-mysql-server
// counterpart that preserves its meaning. It names the type as written so
// the client sees which declaration was refused, rather than receiving a
// column silently typed as something else -- a translation that runs but is
// wrong is worse than one that is cleanly rejected.
type UnsupportedColumnTypeError struct {
	Type string
}

// Error returns a human-readable description of the unsupported type.
func (e *UnsupportedColumnTypeError) Error() string {
	return "sql/engine: unsupported PostgreSQL column type: " + e.Type
}
