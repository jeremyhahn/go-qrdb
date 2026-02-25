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

package sdk

// ClientError is returned when a client-level operation fails.
type ClientError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *ClientError) Error() string {
	return "sdk: " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClientError) Unwrap() error {
	return e.Err
}

// ClientClosedError is returned when an operation is attempted on a
// closed client.
type ClientClosedError struct{}

// Error returns a human-readable description.
func (e *ClientClosedError) Error() string {
	return "sdk: client is closed"
}

// NilTransportError is returned when NewClient is called with a nil
// transport.
type NilTransportError struct{}

// Error returns a human-readable description.
func (e *NilTransportError) Error() string {
	return "sdk: transport must not be nil"
}
