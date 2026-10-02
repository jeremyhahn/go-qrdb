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

package wire

import "strconv"

// MethodTooLongError is returned when a method name exceeds MaxMethodLen.
type MethodTooLongError struct {
	Length int
	Max    int
}

// Error returns a human-readable description.
func (e *MethodTooLongError) Error() string {
	return "wire: method name length " + strconv.Itoa(e.Length) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// DatabaseTooLongError is returned when a database scope name exceeds
// MaxDatabaseLen.
type DatabaseTooLongError struct {
	Length int
	Max    int
}

// Error returns a human-readable description.
func (e *DatabaseTooLongError) Error() string {
	return "wire: database name length " + strconv.Itoa(e.Length) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// PayloadTooLargeError is returned when a payload exceeds MaxPayloadSize.
type PayloadTooLargeError struct {
	Size int
	Max  int
}

// Error returns a human-readable description.
func (e *PayloadTooLargeError) Error() string {
	return "wire: payload size " + strconv.Itoa(e.Size) + " exceeds maximum " + strconv.Itoa(e.Max)
}

// ReadError is returned when reading from the wire fails.
type ReadError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ReadError) Error() string {
	return "wire: read error: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ReadError) Unwrap() error {
	return e.Err
}

// WriteError is returned when writing to the wire fails.
type WriteError struct {
	Err error
}

// Error returns a human-readable description.
func (e *WriteError) Error() string {
	return "wire: write error: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *WriteError) Unwrap() error {
	return e.Err
}

// PayloadEncodeError is returned when an error frame's body cannot be
// encoded.
type PayloadEncodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *PayloadEncodeError) Error() string {
	return "wire: error payload encode failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PayloadEncodeError) Unwrap() error {
	return e.Err
}

// PayloadDecodeError is returned when an error frame's body is not a
// well-formed google.rpc.Status.
type PayloadDecodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *PayloadDecodeError) Error() string {
	return "wire: error payload decode failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PayloadDecodeError) Unwrap() error {
	return e.Err
}
