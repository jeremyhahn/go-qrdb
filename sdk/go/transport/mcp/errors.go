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

// initializeError is returned when MCP protocol initialization fails.
type initializeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *initializeError) Error() string {
	return "mcp transport: initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *initializeError) Unwrap() error {
	return e.Err
}

// toolCallError is returned when an MCP tool call fails.
type toolCallError struct {
	Tool string
	Err  error
}

// Error returns a human-readable description.
func (e *toolCallError) Error() string {
	return "mcp transport: tool " + e.Tool + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *toolCallError) Unwrap() error {
	return e.Err
}

// toolResultError is returned when an MCP tool reports an error in its result.
type toolResultError struct {
	Tool    string
	Message string
}

// Error returns a human-readable description.
func (e *toolResultError) Error() string {
	return "mcp transport: tool " + e.Tool + " returned error: " + e.Message
}

// jsonDecodeError is returned when JSON response parsing fails.
type jsonDecodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *jsonDecodeError) Error() string {
	return "mcp transport: JSON decode failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *jsonDecodeError) Unwrap() error {
	return e.Err
}

// noContentError is returned when a tool result has no content.
type noContentError struct {
	Tool string
}

// Error returns a human-readable description.
func (e *noContentError) Error() string {
	return "mcp transport: tool " + e.Tool + " returned no content"
}

// unexpectedContentTypeError is returned when a tool result contains
// non-text content.
type unexpectedContentTypeError struct {
	Tool string
}

// Error returns a human-readable description.
func (e *unexpectedContentTypeError) Error() string {
	return "mcp transport: tool " + e.Tool + " returned unexpected content type"
}

// jsonEncodeError is returned when JSON request encoding fails.
type jsonEncodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *jsonEncodeError) Error() string {
	return "mcp transport: JSON encode failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *jsonEncodeError) Unwrap() error {
	return e.Err
}

// nilRequestError is returned when a transport method is called with a nil
// request struct.
type nilRequestError struct {
	Method string
}

// Error returns a human-readable description.
func (e *nilRequestError) Error() string {
	return "mcp transport: " + e.Method + ": nil request"
}

// mcpMissingTenantIDError is returned when a tenant-scoped MCP method is
// called without a tenant identifier.
type mcpMissingTenantIDError struct{}

// Error returns a human-readable description.
func (e *mcpMissingTenantIDError) Error() string {
	return "mcp transport: missing tenant_id"
}
