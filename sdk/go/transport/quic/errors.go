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

import (
	"errors"
	"strconv"
	"strings"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// dialError is returned when the QUIC dial operation fails.
type dialError struct {
	Err error
}

// Error returns a human-readable description.
func (e *dialError) Error() string {
	return "quic transport: dial failed: " + e.Err.Error()
}

// healthCheckError reports that a connection was established but the server
// did not answer the health check that confirms it. Err is the probe's
// failure; it is nil when the server answered with an empty status.
type healthCheckError struct {
	Err error
}

// Error returns a human-readable description.
func (e *healthCheckError) Error() string {
	if e.Err == nil {
		return "health check failed: empty status"
	}
	return "health check failed: " + e.Err.Error()
}

// Unwrap returns the probe's failure.
func (e *healthCheckError) Unwrap() error {
	return e.Err
}

// connectBoundError reports that Connect spent the caller's bound without a
// healthy connection. Err is the bound's context error (deadline or
// cancellation) and Last the failure of the last attempt the bound did not
// cut short.
type connectBoundError struct {
	Attempts int
	Err      error
	Last     error
}

// Error returns a human-readable description.
func (e *connectBoundError) Error() string {
	return "quic transport: no healthy connection after " + strconv.Itoa(e.Attempts) +
		" attempt(s): " + e.Err.Error() + "; last attempt: " + e.Last.Error()
}

// Unwrap returns the bound's context error and the last attempt's failure.
func (e *connectBoundError) Unwrap() []error {
	return []error{e.Err, e.Last}
}

// Unwrap returns the underlying error.
func (e *dialError) Unwrap() error {
	return e.Err
}

// alpnMismatchError is returned by Connect when the TLS handshake selected a
// protocol outside wire.Protocols -- possible only when a caller-supplied
// TLS config offers others and the peer is not a qrdb QUIC listener.
type alpnMismatchError struct {
	Negotiated string
}

// Error returns a human-readable description.
func (e *alpnMismatchError) Error() string {
	return "quic transport: server selected ALPN protocol " + strconv.Quote(e.Negotiated) + ", want " + strconv.Quote(wire.ProtocolV3)
}

// noApplicationProtocolCode is the QUIC CRYPTO_ERROR carrying the TLS
// no_application_protocol alert (120): 0x100 + 120 (RFC 9001 section 4.8).
const noApplicationProtocolCode = quicgo.TransportErrorCode(0x100 + 120)

// noApplicationProtocol reports whether err is a handshake the server ended
// because it speaks none of the ALPN protocols the client offered.
func noApplicationProtocol(err error) bool {
	var te *quicgo.TransportError
	return errors.As(err, &te) && te.ErrorCode == noApplicationProtocolCode
}

// protocolRefusedError is returned by Connect when the server refuses the
// handshake with the TLS no_application_protocol alert (120): it speaks
// none of the frame protocols this client offered, so it runs a release
// outside the adjacent range (SPEC row rolling-upgrades). It is not
// retried.
type protocolRefusedError struct {
	// Offered is the ALPN list this client offered, current first.
	Offered []string
	// Err is the QUIC transport error that carried the alert.
	Err error
}

// Error names the protocols offered.
func (e *protocolRefusedError) Error() string {
	return "quic transport: server refused every offered protocol " + strconv.Quote(strings.Join(e.Offered, ",")) +
		" (no_application_protocol): it runs a release that is not adjacent to this client's"
}

// Unwrap returns the QUIC transport error.
func (e *protocolRefusedError) Unwrap() error {
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
