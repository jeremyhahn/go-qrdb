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
	"strconv"

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

// ListenerHostMismatchError is returned when the HTTP/3 (QUIC) socket lands
// on a different bind host than the TCP listener it shadows -- one wildcard,
// the other specific. The two transports serve the same handler, so the
// operator's bind decision has to hold on both; go-qrdb#165 is the case where
// it did not and nothing in the configuration said so.
type ListenerHostMismatchError struct {
	// TCPHost is the host the TCP listener bound.
	TCPHost string
	// UDPHost is the host the QUIC socket bound.
	UDPHost string
}

// Error returns a human-readable description.
func (e *ListenerHostMismatchError) Error() string {
	return "rest: HTTP/3 bind host " + e.UDPHost +
		" does not match REST bind host " + e.TCPHost +
		"; the two transports serve the same handler and must share a bind host"
}

// ListenerPortMismatchError is returned by Start when a pre-bound HTTP/3
// socket (WithPacketConn) is not on the TCP listener's port. The TCP side
// advertises HTTP/3 through Alt-Svc on its own port, so a QUIC socket on any
// other port would be one no client is told about.
type ListenerPortMismatchError struct {
	// TCPPort is the port the TCP listener holds.
	TCPPort string
	// UDPPort is the port the pre-bound QUIC socket holds.
	UDPPort string
}

// Error returns a human-readable description.
func (e *ListenerPortMismatchError) Error() string {
	return "rest: HTTP/3 socket port " + e.UDPPort +
		" does not match REST listener port " + e.TCPPort +
		"; Alt-Svc advertises HTTP/3 on the listener's port"
}

// PacketConnWithoutListenerError is returned by Start when WithPacketConn was
// given without WithListener: the HTTP/3 socket must sit on the TCP
// listener's host and port, which a server that binds its own listener
// cannot promise for a socket bound beforehand.
type PacketConnWithoutListenerError struct{}

// Error returns a human-readable description.
func (e *PacketConnWithoutListenerError) Error() string {
	return "rest: WithPacketConn requires WithListener"
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
// Hint, when set, names the remedy (e.g. "requires mode: raft").
type ServiceNotImplementedError struct {
	Service string
	Hint    string
}

// Error returns a human-readable description.
func (e *ServiceNotImplementedError) Error() string {
	msg := "rest: service not implemented: " + e.Service
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
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

// RequestTooLargeError is returned when a request body exceeds the byte bound
// its endpoint accepts. It is a 413, not a 400: the request was well-formed
// enough to read, and the caller can act on it by sending less.
//
// It carries the bound rather than only saying "too large", because a caller
// that cannot see the limit cannot choose a size that will be accepted.
type RequestTooLargeError struct {
	// Limit is the maximum body size in bytes the endpoint accepts.
	Limit int64
}

// Error returns a human-readable description.
func (e *RequestTooLargeError) Error() string {
	return "rest: request body exceeds the " + strconv.FormatInt(e.Limit, 10) + "-byte limit for this endpoint"
}

// PayloadTooLargeError is returned when a request's DECODED content — its key
// plus every value it carries, summed — exceeds what one Raft entry can hold.
//
// It is distinct from RequestTooLargeError, which reports the coarse bound on
// the encoded body: a request can sit under the wire bound and still decode to
// more content than can be proposed, and a caller told only "your body was too
// big" would shrink the wrong thing.
type PayloadTooLargeError struct {
	// Size is the decoded content size in bytes.
	Size int
	// Limit is the maximum decoded content size in bytes.
	Limit int
}

// Error returns a human-readable description.
func (e *PayloadTooLargeError) Error() string {
	return "rest: request key and values total " + strconv.Itoa(e.Size) +
		" bytes, over the " + strconv.Itoa(e.Limit) + "-byte limit one proposal can carry"
}

// KeyTooLargeError is returned when a guarded request's key exceeds what the
// protocol can frame. It is distinct from PayloadTooLargeError so a caller
// learns WHICH field to shorten: the key ceiling is a two-byte length prefix
// in the command encoding, not a share of the request budget.
type KeyTooLargeError struct {
	// Size is the key length in bytes.
	Size int
	// Limit is the maximum key length in bytes.
	Limit int
}

// Error returns a human-readable description.
func (e *KeyTooLargeError) Error() string {
	return "rest: key is " + strconv.Itoa(e.Size) + " bytes, over the " +
		strconv.Itoa(e.Limit) + "-byte limit the command encoding can frame"
}

// TrailingDataError is returned when a request body carries more than one
// top-level JSON value.
//
// A JSON decoder stops at the end of the first value and leaves the rest
// unread, so without this check a body like `{...}{...}` would perform the
// first operation and report success on malformed input — a fail-open parse
// path on a write endpoint.
type TrailingDataError struct{}

// Error returns a human-readable description.
func (e *TrailingDataError) Error() string {
	return "rest: request body carries data after the first JSON value"
}
