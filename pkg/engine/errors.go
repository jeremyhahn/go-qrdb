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

package engine

// NotOpenError is returned when an engine operation is attempted before
// the engine has been initialized.
type NotOpenError struct{}

func (e *NotOpenError) Error() string {
	return "engine: not open"
}

// UnsupportedOperationError is returned when an engine receives an opcode
// it cannot handle.
type UnsupportedOperationError struct {
	Op byte
}

func (e *UnsupportedOperationError) Error() string {
	return "engine: unsupported operation: 0x" + hexByte(e.Op)
}

// InvalidQueryError is returned when a Lookup receives an unsupported
// query type or invalid parameters.
type InvalidQueryError struct {
	Reason string
}

func (e *InvalidQueryError) Error() string {
	return "engine: invalid query: " + e.Reason
}

// UniqueConstraintError is returned when a write would map a second key to a
// value already held under a unique secondary index. It is an APPLICATION
// error (a deterministic consequence of valid client input), so update
// handlers surface it to the client via the result payload rather than
// returning it from Update — which would stall consensus. See
// protocol.EncodeAppError.
type UniqueConstraintError struct {
	Index string
	Field string
}

func (e *UniqueConstraintError) Error() string {
	return "engine: unique constraint violation on index " + e.Index + " (field " + e.Field + ")"
}

// hexByte converts a byte to its two-character hexadecimal representation.
func hexByte(b byte) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[b>>4], hex[b&0x0f]})
}
