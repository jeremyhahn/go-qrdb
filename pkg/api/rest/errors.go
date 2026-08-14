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

import (
	"crypto/tls"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

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

// ServiceNotImplementedError is returned when a request targets a service
// that was not configured on the server. Optional services (MVCC,
// Transaction, Batch, Watch, Lease, Index, TimeSeries) return this when
// their corresponding ServerOption was not applied.
type ServiceNotImplementedError struct {
	Service string
}

// Error returns a human-readable description.
func (e *ServiceNotImplementedError) Error() string {
	return "rest: service not implemented: " + e.Service
}

// MissingParameterError is returned when a required query parameter is
// absent from the request.
type MissingParameterError struct {
	Param string
}

// Error returns a human-readable description.
func (e *MissingParameterError) Error() string {
	return "rest: missing required parameter: " + e.Param
}

// InvalidParameterError is returned when a query parameter cannot be
// parsed to its expected type.
type InvalidParameterError struct {
	Param string
	Err   error
}

// Error returns a human-readable description.
func (e *InvalidParameterError) Error() string {
	return "rest: invalid parameter " + e.Param + ": " + e.Err.Error()
}

// Unwrap returns the underlying parse error.
func (e *InvalidParameterError) Unwrap() error {
	return e.Err
}

// SSENotSupportedError is returned when the HTTP ResponseWriter does not
// support the http.Flusher interface required for Server-Sent Events.
type SSENotSupportedError struct{}

// Error returns a human-readable description.
func (e *SSENotSupportedError) Error() string {
	return "rest: server-sent events not supported by response writer"
}

// InvalidAggFuncError is returned when an unrecognized aggregation function
// name is provided for a time-series aggregate request.
type InvalidAggFuncError struct {
	Func string
}

// Error returns a human-readable description.
func (e *InvalidAggFuncError) Error() string {
	return "rest: invalid aggregation function: " + e.Func
}

// InvalidRetentionDurationError is returned when a retention policy
// duration is zero or negative.
type InvalidRetentionDurationError struct{}

// Error returns a human-readable description.
func (e *InvalidRetentionDurationError) Error() string {
	return "rest: retention duration must be positive"
}

// EmbeddingEmptyTextsError is returned when an embedding request contains
// no input texts.
type EmbeddingEmptyTextsError struct{}

// Error returns a human-readable description.
func (e *EmbeddingEmptyTextsError) Error() string {
	return "rest: embedding request must contain at least one text"
}

// DatabaseScopeConflictError is returned when a request names one database
// in its body and a different one in the X-QRDB-Database scope header.
// Maps to HTTP 400.
type DatabaseScopeConflictError struct {
	Body   string
	Header string
}

// Error returns a human-readable description.
func (e *DatabaseScopeConflictError) Error() string {
	return "rest: database scope conflict: body names " + e.Body +
		" but " + HeaderDatabase + " header names " + e.Header
}

// TenantNotFoundError is returned when the tenant ID from the authenticated
// identity does not match any provisioned tenant. Maps to HTTP 404. It is an
// alias of the transport-neutral service.TenantNotFoundError so the shared
// registry implementation can be consumed by every protocol.
type TenantNotFoundError = service.TenantNotFoundError

// DatabaseNotFoundError is returned when the database name from
// X-QRDB-Database does not exist within the resolved tenant. Maps to HTTP 404.
// Alias of service.DatabaseNotFoundError.
type DatabaseNotFoundError = service.DatabaseNotFoundError

// BarrierSealedError is returned when the tenant's active barrier is sealed,
// preventing data-plane access. Maps to HTTP 503. Alias of
// service.BarrierSealedError.
type BarrierSealedError = service.BarrierSealedError

// IsolationBackendUnavailableError is returned when a tenant's isolation mode
// requires a dedicated storage backend that is not wired into this server.
// Maps to HTTP 503. Alias of service.IsolationBackendUnavailableError.
type IsolationBackendUnavailableError = service.IsolationBackendUnavailableError

// ValidationError is returned when a required field is absent or invalid in
// the request payload. Maps to HTTP 400.
type ValidationError struct {
	Field string
}

// Error returns a human-readable description.
func (e *ValidationError) Error() string {
	return "rest: validation error: " + e.Field
}

// TLSNotConfiguredError is returned by Start when no TLS configuration was
// supplied via WithTLS. qrdb transports are TLS-only — the mTLS client
// certificate is the identity the auth middleware authenticates against — so
// the REST server fails closed rather than bind an unauthenticated plaintext
// listener. The orchestrator (pkg/server) always resolves a config, either the
// operator-supplied one or a certificate auto-provisioned from the node's
// CeremonyCA, so a normal deployment supplies a config and never triggers this.
type TLSNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *TLSNotConfiguredError) Error() string {
	return "rest: no TLS configured; the REST transport is TLS-only, call WithTLS with a TLS 1.3+ config"
}

// WeakTLSVersionError is returned by Start when WithTLS was given a
// *tls.Config whose MinVersion is explicitly set below TLS 1.3. TLS 1.3 is
// the minimum version this codebase supports for the client-certificate
// identity model the auth middleware relies on.
type WeakTLSVersionError struct {
	MinVersion uint16
}

// Error returns a human-readable description.
func (e *WeakTLSVersionError) Error() string {
	return "rest: TLS MinVersion " + tlsVersionName(e.MinVersion) + " is below the required TLS 1.3"
}

// tlsVersionName renders a crypto/tls version constant (or 0 for "unset")
// as a human-readable string for error messages.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	case 0:
		return "unset"
	default:
		return "unknown"
	}
}
