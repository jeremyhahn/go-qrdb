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

package main

// TransportError is returned when transport construction fails.
type TransportError struct {
	Transport string
	Err       error
}

// Error returns a human-readable description.
func (e *TransportError) Error() string {
	return "qrdb: transport " + e.Transport + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TransportError) Unwrap() error {
	return e.Err
}

// ConnectError is returned when connection to the server fails.
type ConnectError struct {
	Address string
	Err     error
}

// Error returns a human-readable description.
func (e *ConnectError) Error() string {
	return "qrdb: connect to " + e.Address + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConnectError) Unwrap() error {
	return e.Err
}

// OutputFormatError is returned when an unknown output format is specified.
type OutputFormatError struct {
	Format string
}

// Error returns a human-readable description.
func (e *OutputFormatError) Error() string {
	return "qrdb: unknown output format: " + e.Format
}

// MissingArgError is returned when a required argument is not provided.
type MissingArgError struct {
	Arg string
}

// Error returns a human-readable description.
func (e *MissingArgError) Error() string {
	return "qrdb: missing required argument: " + e.Arg
}

// InvalidArgError is returned when an argument has an invalid value.
type InvalidArgError struct {
	Arg    string
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidArgError) Error() string {
	return "qrdb: invalid argument " + e.Arg + ": " + e.Reason
}
