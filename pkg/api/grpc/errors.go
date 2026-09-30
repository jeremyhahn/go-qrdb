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

import (
	"crypto/tls"
	"fmt"
)

// StartError is returned when the gRPC server fails to start listening
// on the configured address.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "grpc: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// ServiceNotConfiguredError is returned when a gRPC handler is invoked
// for a service that was not provided during server construction. Hint,
// when set, names the remedy (e.g. "requires mode: raft").
type ServiceNotConfiguredError struct {
	Service string
	Hint    string
}

// Error returns a human-readable description.
func (e *ServiceNotConfiguredError) Error() string {
	msg := "grpc: " + e.Service + " service is not configured"
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
}

// CeremonyCANotConfiguredError is returned when CeremonyInitialize is
// invoked but the server was not configured with a CeremonyCA (WithCeremonyCA
// for the TCP gRPC transport, WithRegisterCeremonyCA for the Unix domain
// socket transport). The ceremony service dereferences the CA to issue
// certificates, so failing closed here avoids a nil-pointer panic.
type CeremonyCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *CeremonyCANotConfiguredError) Error() string {
	return "grpc: ceremony CA is not configured"
}

// MetadataMarshalError is returned when metadata cannot be serialized to JSON
// for a gRPC response.
type MetadataMarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MetadataMarshalError) Error() string {
	return "grpc: failed to marshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetadataMarshalError) Unwrap() error {
	return e.Err
}

// MetadataUnmarshalError is returned when metadata JSON from a gRPC request
// cannot be deserialized.
type MetadataUnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MetadataUnmarshalError) Error() string {
	return "grpc: failed to unmarshal metadata: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MetadataUnmarshalError) Unwrap() error {
	return e.Err
}

// VectorIDParseError is returned when a vector ID string cannot be parsed
// as a uint64 identifier.
type VectorIDParseError struct {
	ID  string
	Err error
}

// Error returns a human-readable description.
func (e *VectorIDParseError) Error() string {
	return "grpc: failed to parse vector ID " + e.ID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorIDParseError) Unwrap() error {
	return e.Err
}

// TTLParseError is returned when a TTL duration string cannot be parsed.
type TTLParseError struct {
	Value string
	Err   error
}

// Error returns a human-readable description.
func (e *TTLParseError) Error() string {
	return "grpc: invalid TTL duration " + e.Value + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TTLParseError) Unwrap() error {
	return e.Err
}

// TimestampParseError is returned when an RFC 3339 timestamp field in a
// request does not parse.
type TimestampParseError struct {
	Field string
	Value string
	Err   error
}

// Error returns a human-readable description naming the field.
func (e *TimestampParseError) Error() string {
	return "grpc: invalid " + e.Field + " " + e.Value + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TimestampParseError) Unwrap() error {
	return e.Err
}

// TLSNotConfiguredError is returned by Start when no TLS configuration was
// supplied via WithTLS. The gRPC transport is TLS-only — the mTLS client
// certificate is the identity the auth middleware authenticates against — so
// Start fails closed rather than bind an unauthenticated plaintext listener.
// The orchestrator (pkg/server) auto-provisions a certificate from the node's
// CeremonyCA, so a normal deployment supplies a config and never triggers this.
type TLSNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *TLSNotConfiguredError) Error() string {
	return "grpc: no TLS configured; the gRPC transport is TLS-only, call WithTLS with a TLS 1.3+ config"
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
	return "grpc: TLS MinVersion " + tlsVersionName(e.MinVersion) + " is below the required TLS 1.3"
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
		return fmt.Sprintf("0x%04x", v)
	}
}
