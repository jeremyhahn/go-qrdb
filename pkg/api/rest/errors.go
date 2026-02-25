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

// StartError is returned when the REST HTTP server fails to start
// listening on the configured address.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "rest: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// ReadBodyError is returned when reading an HTTP request body fails.
type ReadBodyError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ReadBodyError) Error() string {
	return "rest: read body failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ReadBodyError) Unwrap() error {
	return e.Err
}

// LeaderForwardError is returned when proxying a write request to the
// leader's REST server fails. The underlying error describes the network
// or protocol failure.
type LeaderForwardError struct {
	LeaderURL string
	Err       error
}

// Error returns a human-readable description.
func (e *LeaderForwardError) Error() string {
	return "rest: leader forward failed to " + e.LeaderURL + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LeaderForwardError) Unwrap() error {
	return e.Err
}

// LeaderForwardLoopError is returned when a forwarded request reaches a
// node that would forward it again, indicating an infinite forwarding loop.
type LeaderForwardLoopError struct {
	LeaderURL string
}

// Error returns a human-readable description.
func (e *LeaderForwardLoopError) Error() string {
	return "rest: leader forward loop detected, target " + e.LeaderURL
}

// HTTP3StartError is returned when the HTTP/3 (QUIC) server fails to start.
type HTTP3StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *HTTP3StartError) Error() string {
	return "rest: HTTP/3 server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HTTP3StartError) Unwrap() error {
	return e.Err
}

// FetchError is returned when an HTTP request to a REST server endpoint
// fails during fetching.
type FetchError struct {
	URL string
	Err error
}

// Error returns a human-readable description.
func (e *FetchError) Error() string {
	return "rest: failed to fetch " + e.URL + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *FetchError) Unwrap() error {
	return e.Err
}

// FetchResponseError is returned when a REST server endpoint responds
// with an unexpected HTTP status code.
type FetchResponseError struct {
	URL        string
	StatusCode int
}

// Error returns a human-readable description.
func (e *FetchResponseError) Error() string {
	return "rest: unexpected status " + httpStatusText(e.StatusCode) + " for " + e.URL
}

// httpStatusText returns the status code as a string for error messages.
func httpStatusText(code int) string {
	// Avoid importing strconv for a trivial conversion.
	buf := [3]byte{
		byte('0' + code/100%10),
		byte('0' + code/10%10),
		byte('0' + code%10),
	}
	return string(buf[:])
}

// ProposeError is returned when a write proposal through the REST API fails.
type ProposeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ProposeError) Error() string {
	return "rest: propose failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ProposeError) Unwrap() error {
	return e.Err
}

// ReadError is returned when a read operation through the REST API fails.
type ReadError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ReadError) Error() string {
	return "rest: read failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ReadError) Unwrap() error {
	return e.Err
}

// MembershipError is returned when a membership change operation through
// the REST API fails.
type MembershipError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *MembershipError) Error() string {
	return "rest: membership " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MembershipError) Unwrap() error {
	return e.Err
}

// SnapshotRequestError is returned when a snapshot request through the
// REST API fails.
type SnapshotRequestError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SnapshotRequestError) Error() string {
	return "rest: snapshot request failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SnapshotRequestError) Unwrap() error {
	return e.Err
}
