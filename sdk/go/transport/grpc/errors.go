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

package grpc

// dialError is returned when gRPC dial fails.
type dialError struct {
	Err error
}

// Error returns a human-readable description.
func (e *dialError) Error() string {
	return "grpc transport: dial failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *dialError) Unwrap() error {
	return e.Err
}

// caFileReadError is returned when the CA file cannot be read.
type caFileReadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *caFileReadError) Error() string {
	return "grpc transport: failed to read CA file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *caFileReadError) Unwrap() error {
	return e.Err
}

// caParseError is returned when a CA certificate cannot be parsed.
type caParseError struct {
	Path string
}

// Error returns a human-readable description.
func (e *caParseError) Error() string {
	return "grpc transport: failed to parse CA certificate from " + e.Path
}

// certLoadError is returned when a client certificate cannot be loaded.
type certLoadError struct {
	Err error
}

// Error returns a human-readable description.
func (e *certLoadError) Error() string {
	return "grpc transport: failed to load client certificate: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *certLoadError) Unwrap() error {
	return e.Err
}
