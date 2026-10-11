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

package mcp

import (
	"crypto/tls"
)

// StartError is returned when the MCP server fails to start listening
// on the configured address.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "mcp: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// TLSNotConfiguredError is returned by Start when no TLS configuration was
// supplied via WithTLS. qrdb transports are TLS-only — the mTLS client
// certificate is the identity the auth middleware authenticates against — so
// the MCP server fails closed rather than bind an unauthenticated plaintext
// listener. The orchestrator (pkg/server) always resolves a config, either the
// operator-supplied one or a certificate auto-provisioned from the node's
// CeremonyCA, so a normal deployment supplies a config and never triggers this.
type TLSNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *TLSNotConfiguredError) Error() string {
	return "mcp: no TLS configured; the MCP transport is TLS-only, call WithTLS with a TLS 1.3+ config"
}

// UnknownPublicToolError is returned by Start when a module's
// apimodule.MCPPublicToolLister declares a tool no registration on the server
// answers. Refusing the typo at startup keeps the intended tool from silently
// staying behind authentication.
type UnknownPublicToolError struct {
	Tool string
}

// Error returns a human-readable description.
func (e *UnknownPublicToolError) Error() string {
	return "mcp: tool " + e.Tool + " is declared public but is not registered on the server"
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
	return "mcp: TLS MinVersion " + tlsVersionName(e.MinVersion) + " is below the required TLS 1.3"
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

// CeremonyCANotConfiguredError is returned when a ceremony operation
// requires a CA but one was not configured.
type CeremonyCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *CeremonyCANotConfiguredError) Error() string {
	return "mcp: ceremony CA not configured"
}

// ErrCeremonyCANotConfigured is a convenience instance.
var ErrCeremonyCANotConfigured = &CeremonyCANotConfiguredError{}

// ServiceNotConfiguredError is returned when a data-plane tool resolves
// neither a per-(tenant, database) registry nor a fallback singleton for the
// service it needs. It is the fail-closed answer to a tool that was registered
// because a registry was configured but invoked without the scope middleware
// that binds one -- reported rather than dereferenced.
type ServiceNotConfiguredError struct {
	// Service names the unresolvable service, e.g. "collection".
	Service string
}

// Error returns a human-readable description.
func (e *ServiceNotConfiguredError) Error() string {
	return "mcp: service not configured: " + e.Service
}

// ToolError is returned when an MCP tool execution fails.
type ToolError struct {
	Tool string
	Err  error
}

// Error returns a human-readable description.
func (e *ToolError) Error() string {
	return "mcp: tool " + e.Tool + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ToolError) Unwrap() error {
	return e.Err
}

// RequiredParameterError is returned by tool argument parsing when a required
// parameter is absent from the request.
type RequiredParameterError struct {
	// Parameter is the name of the missing required parameter.
	Parameter string
}

// Error returns "<parameter> is required".
func (e *RequiredParameterError) Error() string {
	return "mcp: " + e.Parameter + " is required"
}

// ImportFileDecodeError is returned when a chunked import session tool's
// base64-encoded "file" or "chunk" argument cannot be decoded.
type ImportFileDecodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ImportFileDecodeError) Error() string {
	return "mcp: file must be base64-encoded: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ImportFileDecodeError) Unwrap() error {
	return e.Err
}

// ValueDecodeError is returned when a tool's "value" (or similarly named)
// argument cannot be base64-decoded (RFC 4648). KV and MVCC value fields are
// carried as base64 text over MCP so arbitrary binary content round-trips
// byte-for-byte -- see kv put/get/scan and mvcc put-with-lease.
type ValueDecodeError struct {
	// Field is the argument name that failed to decode (e.g. "value").
	Field string
	Err   error
}

// Error returns a human-readable description.
func (e *ValueDecodeError) Error() string {
	return "mcp: " + e.Field + " must be valid base64: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ValueDecodeError) Unwrap() error {
	return e.Err
}

// ToolArgumentError reports a tool argument whose SHAPE is wrong — an array
// where a scalar arrived, or a non-string element in a list of identifiers.
//
// It is distinct from a validation failure inside the store: nothing was
// attempted, because the argument could not be read as the type the tool
// declares. Coercing instead would be worse than refusing — these lists carry
// opaque identifiers, and a value the caller did not write is not an
// improvement on an error.
type ToolArgumentError struct {
	Argument string
	Reason   string
}

// Error returns a human-readable description naming the argument.
func (e *ToolArgumentError) Error() string {
	return "mcp: argument " + e.Argument + ": " + e.Reason
}
