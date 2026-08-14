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

package unix

// StartError is returned when the Unix domain socket gRPC server fails
// to start listening on the configured socket path.
type StartError struct {
	SocketPath string
	Err        error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "unix: server failed to start on " + e.SocketPath + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// MkdirError is returned when the server cannot create the parent
// directory for the socket path.
type MkdirError struct {
	Dir string
	Err error
}

// Error returns a human-readable description.
func (e *MkdirError) Error() string {
	return "unix: failed to create socket directory " + e.Dir + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MkdirError) Unwrap() error {
	return e.Err
}

// TLSNotConfiguredError is returned by Start when no TLS configuration was
// supplied via WithTLS. The Unix transport is TLS-only — the mTLS client
// certificate is the identity the auth middleware authenticates against — so
// Start fails closed rather than bind an unauthenticated plaintext listener.
// The orchestrator (pkg/server) auto-provisions a certificate from the node's
// CeremonyCA, so a normal deployment supplies a config and never triggers this.
type TLSNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *TLSNotConfiguredError) Error() string {
	return "unix: no TLS configured; the Unix transport is TLS-only, call WithTLS with a TLS 1.3+ config"
}

// WeakTLSVersionError is returned by Start when WithTLS was given a
// *tls.Config whose MinVersion is explicitly set below TLS 1.3. The Unix
// transport requires TLS 1.3, the minimum version this codebase supports for
// the client-certificate identity model the auth middleware relies on.
type WeakTLSVersionError struct {
	MinVersion uint16
}

// Error returns a human-readable description.
func (e *WeakTLSVersionError) Error() string {
	return "unix: TLS MinVersion " + tlsVersionName(e.MinVersion) + " is below the required TLS 1.3"
}
