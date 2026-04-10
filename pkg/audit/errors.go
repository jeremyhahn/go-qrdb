// Copyright (c) 2026 Jeremy Hahn
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

package audit

// WriteError is returned when writing an audit entry to a backend fails.
type WriteError struct {
	Backend string
	Err     error
}

// Error returns a human-readable description of the write failure.
func (e *WriteError) Error() string {
	return "audit: write to " + e.Backend + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *WriteError) Unwrap() error { return e.Err }

// FileError is returned when the audit log file cannot be opened or created.
type FileError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the file operation failure.
func (e *FileError) Error() string {
	return "audit: cannot open file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *FileError) Unwrap() error { return e.Err }

// ConfigError is returned when the audit configuration is invalid.
type ConfigError struct {
	Detail string
}

// Error returns a human-readable description of the configuration error.
func (e *ConfigError) Error() string {
	return "audit: invalid config: " + e.Detail
}
