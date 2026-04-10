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

package mcp

// StartError is returned when the MCP server fails to start listening
// on the configured address.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "mcp: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// CeremonyCANotConfiguredError is returned when a ceremony operation
// requires a CA but one was not configured.
type CeremonyCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *CeremonyCANotConfiguredError) Error() string {
	return "mcp: ceremony CA not configured"
}

// ErrCeremonyCANotConfigured is a convenience instance.
var ErrCeremonyCANotConfigured = &CeremonyCANotConfiguredError{}

// ToolError is returned when an MCP tool execution fails.
type ToolError struct {
	Tool string
	Err  error
}

// Error returns a human-readable description.
func (e *ToolError) Error() string {
	return "mcp: tool " + e.Tool + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ToolError) Unwrap() error {
	return e.Err
}
