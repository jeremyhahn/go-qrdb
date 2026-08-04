// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package remote

import "errors"

var (
	// ErrNilConfig is returned when NewRemoteGPUIndex is called with a nil Config.
	ErrNilConfig = errors.New("remote gpu index: config must not be nil")

	// ErrEmptyEndpoint is returned when the Config endpoint is empty.
	ErrEmptyEndpoint = errors.New("remote gpu index: endpoint must not be empty")
)

// IndexError wraps a remote GPU index operation failure together with the
// underlying gRPC (or dial) cause. It preserves errors.Is/errors.As against
// the cause, replacing the inline "remote gpu index: <op>: %w" error-wrapping idiom.
type IndexError struct {
	// Op names the operation that failed (e.g. "insert id=42", "search k=10",
	// "dial 1.2.3.4:9090").
	Op string
	// Cause is the underlying error.
	Cause error
}

// Error returns "remote gpu index: <op>: <cause>".
func (e *IndexError) Error() string {
	return "remote gpu index: " + e.Op + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *IndexError) Unwrap() error { return e.Cause }

// TLSConfigError wraps a failure encountered while building the mTLS dial
// credentials for the remote VectorIndexService connection.
type TLSConfigError struct {
	// Op names the step that failed (e.g. "load client key pair",
	// "read CA cert /path/ca.pem").
	Op string
	// Cause is the underlying error.
	Cause error
}

// Error returns "<op>: <cause>".
func (e *TLSConfigError) Error() string {
	return e.Op + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *TLSConfigError) Unwrap() error { return e.Cause }

// CACertParseError is returned when a CA certificate file contains no valid
// PEM-encoded certificates.
type CACertParseError struct {
	// Path is the CA certificate file path that failed to parse.
	Path string
}

// Error returns "parse CA cert <path>: no valid certificates found".
func (e *CACertParseError) Error() string {
	return "parse CA cert " + e.Path + ": no valid certificates found"
}

// MarshalError wraps a failure to JSON-encode a request before sending it to
// the remote VectorIndexService.
type MarshalError struct {
	// Method is the gRPC full method name whose request failed to marshal.
	Method string
	// Cause is the underlying marshalling error.
	Cause error
}

// Error returns "marshal request for <method>: <cause>".
func (e *MarshalError) Error() string {
	return "marshal request for " + e.Method + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *MarshalError) Unwrap() error { return e.Cause }

// UnmarshalError wraps a failure to decode a response received from the remote
// VectorIndexService.
type UnmarshalError struct {
	// Method is the gRPC full method name whose response failed to unmarshal.
	Method string
	// Cause is the underlying unmarshalling error.
	Cause error
}

// Error returns "unmarshal response for <method>: <cause>".
func (e *UnmarshalError) Error() string {
	return "unmarshal response for " + e.Method + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *UnmarshalError) Unwrap() error { return e.Cause }
