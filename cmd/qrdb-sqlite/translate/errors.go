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

package translate

// PragmaQuery is returned by Translator.Translate when the input SQL is a
// SQLite PRAGMA statement. It implements the error interface so that callers
// can use a single return-value path and type-assert to distinguish PRAGMA
// queries from translation errors.
//
// Name is the pragma name (e.g. "qrdb_consistency").
// Value is the value from a setter form (e.g. "eventual"); it is empty for
// getter forms.
type PragmaQuery struct {
	Name  string
	Value string
}

// Error returns a human-readable description of the PRAGMA query. Callers
// that receive a non-nil error from Translate should type-assert to
// *PragmaQuery before treating the return value as a real error.
func (p *PragmaQuery) Error() string {
	if p.Value != "" {
		return "translate: PRAGMA " + p.Name + " = " + p.Value
	}
	return "translate: PRAGMA " + p.Name
}

// EmptyQueryError is returned by Translator.Translate when the input query
// is blank or contains only whitespace.
type EmptyQueryError struct{}

// Error returns a human-readable description of the empty query error.
func (e *EmptyQueryError) Error() string {
	return "translate: query must not be empty"
}
