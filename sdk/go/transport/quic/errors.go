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

package quic

import "strconv"

// dialError is returned when the QUIC dial operation fails.
type dialError struct {
	Err error
}

// Error returns a human-readable description.
func (e *dialError) Error() string {
	return "quic transport: dial failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *dialError) Unwrap() error {
	return e.Err
}

// streamError is returned when opening a QUIC stream fails.
type streamError struct {
	Err error
}

// Error returns a human-readable description.
func (e *streamError) Error() string {
	return "quic transport: stream open failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *streamError) Unwrap() error {
	return e.Err
}

// protocolError is returned when the QUIC server responds with a
// non-OK wire status code.
type protocolError struct {
	Method     string
	StatusCode uint16
	Message    string
}

// Error returns a human-readable description.
func (e *protocolError) Error() string {
	return "quic transport: " + e.Method + " failed (status " + strconv.FormatUint(uint64(e.StatusCode), 10) + "): " + e.Message
}
