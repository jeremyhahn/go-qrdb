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

	// Err is the typed error the field's own validation reported, when it
	// has one; nil otherwise.
	Err error
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "config: validation failed for " + strconv.Quote(e.Field) + ": " + e.Reason
}

// Unwrap returns the field's own validation error for use with errors.Is and
// errors.As, or nil.
func (e *ValidationError) Unwrap() error {
	return e.Err
}

// UnknownFieldError is returned when a configuration file carries a key that
// does not name any field of the Config schema -- a stale key left behind by
// a rename, or simply a typo. The loader refuses the file rather than
// silently discarding the key and running with whatever the key was
// supposed to turn on left off.
//
// A key for a feature this binary was not compiled with (e.g.
// "protocols.mysql" without build tag qrdb_mysql) is never reported here:
// pkg/config declares every field unconditionally, and that refusal is
// Config.ValidateCapabilities's "not compiled in" error instead.
type UnknownFieldError struct {
	// File is the path of the configuration file that carried the key.
	File string

	// Path is the key's full path from the document root, dot-separated
	// through nested sections and bracket-indexed through sequences, e.g.
	// "databases[2].protocol".
	Path string

	// Line is the key's 1-based line number within File, when known. Zero
	// means unknown.
	Line int
}

// Error returns a human-readable description of the unknown key, naming its
// full path and, when known, its location in the file.
func (e *UnknownFieldError) Error() string {
	msg := "config: unknown key " + strconv.Quote(e.Path) + " in " + e.File
	if e.Line > 0 {
		msg += ":" + strconv.Itoa(e.Line)
	}
	return msg
}

// DatabaseConfigError is returned when a database configuration entry is invalid.
type DatabaseConfigError struct {
	// Name is the database name that failed validation.
	Name string

	// Message describes why the database configuration is invalid.
	Message string
}

// Error returns a human-readable description of the database config failure.
func (e *DatabaseConfigError) Error() string {
	return "config: invalid database " + strconv.Quote(e.Name) + ": " + e.Message
}
