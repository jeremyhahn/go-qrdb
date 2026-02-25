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

package config

import "strconv"

// LoadError is returned when a configuration file cannot be read or parsed.
type LoadError struct {
	// Path is the file path that failed to load.
	Path string

	// Err is the underlying filesystem or YAML parse error.
	Err error
}

// Error returns a human-readable description of the load failure.
func (e *LoadError) Error() string {
	return "config: failed to load " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *LoadError) Unwrap() error {
	return e.Err
}

// ValidationError is returned when a configuration field has an invalid value.
type ValidationError struct {
	// Field is the dot-separated path to the invalid field (e.g., "raft.shard_id").
	Field string

	// Reason describes why the field value is invalid.
	Reason string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "config: validation failed for " + strconv.Quote(e.Field) + ": " + e.Reason
}
