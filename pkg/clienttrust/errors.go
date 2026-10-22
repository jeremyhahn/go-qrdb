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

package clienttrust

// ClusterCANotConfiguredError reports that no ClusterCA resolver is
// installed on the Pool. Key B is never trusted while this is the reason a
// check fails: the pool and the issuer-tenant binding are the same object,
// so Key B's own trust depends on the cluster CA being resolvable too.
type ClusterCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *ClusterCANotConfiguredError) Error() string {
	return "clienttrust: no cluster CA configured; Key B is not trusted"
}

// ClusterCAUnavailableError reports that a ClusterCA resolver is installed
// but failed to produce a certificate on this call. The failure is scoped to
// THIS call: the next connection re-resolves, so a transient error never
// disables Key B trust for the life of the process, only for the connection
// that hit it.
type ClusterCAUnavailableError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterCAUnavailableError) Error() string {
	msg := "clienttrust: cluster CA certificate unavailable"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *ClusterCAUnavailableError) Unwrap() error {
	return e.Err
}

// KeyBUnavailableError reports that Key B material cannot be resolved right
// now (no provider installed, the installed provider does not implement
// CertSource, or CACertPEMs itself failed -- the ordinary outcome while
// sealed in Raft mode). The dynamic trust pool falls back to the static,
// Key-A-only config rather than widening.
type KeyBUnavailableError struct {
	Reason string
	Err    error
}

// Error returns a human-readable description.
func (e *KeyBUnavailableError) Error() string {
	msg := "clienttrust: Key B unavailable: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *KeyBUnavailableError) Unwrap() error {
	return e.Err
}

// RefreshSupersededError reports a Refresh whose every rebuild was
// superseded by a concurrent change before its bound ran out, so no snapshot
// built after the call began was published.
type RefreshSupersededError struct {
	Err error
}

// Error returns a human-readable description.
func (e *RefreshSupersededError) Error() string {
	return "clienttrust: every rebuild was superseded before it could be published: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RefreshSupersededError) Unwrap() error {
	return e.Err
}

// IssuerNotTrustedError reports that a certificate's issuer is neither the
// cluster CA (Key A) nor tenantID's registered Key B CA.
type IssuerNotTrustedError struct {
	TenantID string
	Err      error
}

// Error returns a human-readable description.
func (e *IssuerNotTrustedError) Error() string {
	msg := "clienttrust: issuer is not the cluster CA or tenant " + e.TenantID + "'s registered CA"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *IssuerNotTrustedError) Unwrap() error {
	return e.Err
}

// PeerCertificateNotTrustedError reports that a presented client
// certificate's chain does not verify against the pool's current widened
// trust set. It is returned both by Wire's VerifyConnection hook (crypto/tls
// aborts the handshake) and by VerifyPeerChain (the authenticator's
// per-request re-derivation of the chain crypto/tls no longer builds for a
// connection Wire touched -- see VerifyPeerChain's own doc comment).
type PeerCertificateNotTrustedError struct {
	Err error

	// Unread is the read that failed while the trust set the chain was
	// verified against was built (VerifyPeerChain only), or nil when every
	// configured source answered.
	Unread error
}

// Error returns a human-readable description.
func (e *PeerCertificateNotTrustedError) Error() string {
	msg := "clienttrust: peer certificate chain is not trusted"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Unread != nil {
		msg += " (trust state unavailable: " + e.Unread.Error() + ")"
	}
	return msg
}

// TrustStateUnavailable reports whether the chain was refused while a trust
// source did not answer, so a retry may admit it (pkg/auth).
func (e *PeerCertificateNotTrustedError) TrustStateUnavailable() bool {
	return e.Unread != nil
}

// Unwrap returns the underlying error.
func (e *PeerCertificateNotTrustedError) Unwrap() error {
	return e.Err
}

// StaticClusterCANilCertificateError reports that NewStaticClusterCA was
// called with a nil certificate.
type StaticClusterCANilCertificateError struct{}

// Error returns a human-readable description.
func (e *StaticClusterCANilCertificateError) Error() string {
	return "clienttrust: NewStaticClusterCA requires a non-nil certificate"
}
