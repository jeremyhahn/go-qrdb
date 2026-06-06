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

package rest

// healthCheckError is returned when the initial health check during
// Connect fails.
type healthCheckError struct{}

// Error returns a human-readable description.
func (e *healthCheckError) Error() string {
	return "health check failed"
}

// serverError wraps an error message returned by the QRDB REST server.
type serverError struct {
	Message string
}

// Error returns the server's error message.
func (e *serverError) Error() string {
	return e.Message
}

// jsonEncodeError is returned when JSON marshaling fails.
type jsonEncodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *jsonEncodeError) Error() string {
	return "json encode failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *jsonEncodeError) Unwrap() error {
	return e.Err
}

// jsonDecodeError is returned when JSON unmarshaling fails.
type jsonDecodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *jsonDecodeError) Error() string {
	return "json decode failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *jsonDecodeError) Unwrap() error {
	return e.Err
}

// readBodyError is returned when reading an HTTP response body fails.
type readBodyError struct {
	Err error
}

// Error returns a human-readable description.
func (e *readBodyError) Error() string {
	return "read body failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *readBodyError) Unwrap() error {
	return e.Err
}

// caFileReadError is returned when reading a CA certificate file fails.
type caFileReadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *caFileReadError) Error() string {
	return "failed to read CA file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *caFileReadError) Unwrap() error {
	return e.Err
}

// caParseError is returned when parsing a CA certificate fails.
type caParseError struct {
	Path string
}

// Error returns a human-readable description.
func (e *caParseError) Error() string {
	return "failed to parse CA certificate from " + e.Path
}

// certLoadError is returned when loading a client TLS certificate fails.
type certLoadError struct {
	Err error
}

// Error returns a human-readable description.
func (e *certLoadError) Error() string {
	return "failed to load client certificate: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *certLoadError) Unwrap() error {
	return e.Err
}

// drainFailedError is returned when the drain endpoint responds with
// a non-success status.
type drainFailedError struct {
	Status string
	Detail string
}

// Error returns a human-readable description.
func (e *drainFailedError) Error() string {
	msg := "drain failed with status " + e.Status
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// nilRequestError is returned when a typed request pointer required by an
// SDK method is nil. It signals a programming error in the caller.
type nilRequestError struct{}

// Error returns a human-readable description.
func (e *nilRequestError) Error() string {
	return "request must not be nil"
}

// emptyTenantIDError is returned when an SDK method requires a tenant ID
// but the caller provided an empty string.
type emptyTenantIDError struct{}

// Error returns a human-readable description.
func (e *emptyTenantIDError) Error() string {
	return "tenant id must not be empty"
}

// emptyCredentialNameError is returned when a credential SDK method
// requires a credential name but the caller provided an empty string.
type emptyCredentialNameError struct{}

// Error returns a human-readable description.
func (e *emptyCredentialNameError) Error() string {
	return "credential name must not be empty"
}
