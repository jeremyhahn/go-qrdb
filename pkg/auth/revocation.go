// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"
	"crypto/x509"
	"errors"
)

// RevocationChecker reports whether a verified mTLS peer certificate has been
// revoked.
//
// It is declared here, at the consumer, so pkg/auth does not depend on the
// certificate authority that answers it. pkg/cert.RevocationCache satisfies it
// structurally and is what production wiring supplies
// (pkg/auth/wiring.AuthMiddlewareConfig.CertProvider).
//
// ENFORCEMENT LIVES HERE, NOT IN A TLS CALLBACK. A tls.Config
// VerifyConnection/VerifyPeerCertificate hook fires exactly once, when the
// connection is established. On QUIC — where a client holds one long-lived
// connection and multiplexes every request over it — a peer revoked after the
// handshake would stay authorised for the life of that connection. The
// authenticator runs on every request, which is the only placement that makes
// revocation take effect while a session is already open.
type RevocationChecker interface {
	// IsCertRevoked reports whether peer is revoked.
	//
	// An error for which RevocationStateUnavailable reports true means no
	// answer exists on this node at all, as distinct from a lookup that could
	// have answered and failed. MTLSAuthenticator.Authenticate documents what
	// it does with each.
	IsCertRevoked(ctx context.Context, peer *x509.Certificate) (bool, error)
}

// UncachedRevocationChecker is a RevocationChecker that can also answer
// without trusting a cached not-revoked answer. A delegation's revocation
// refuses its next call, so authenticateDelegation asks this method when
// the checker offers it (pkg/cert.RevocationCache does); ordinary
// certificates keep the checker's cache window.
type UncachedRevocationChecker interface {
	RevocationChecker
	IsCertRevokedUncached(ctx context.Context, peer *x509.Certificate) (bool, error)
}

// EnforceRevocation applies the revocation policy to a verified peer
// certificate and returns the error the caller must fail with, or nil to
// admit.
//
// It is THE single definition of that policy. Two call sites apply it — the
// mTLS authenticator, and the middleware's on-behalf forwarding path, which
// bypasses the authenticator chain entirely — and two copies of a three-way
// fail-open/fail-closed decision is how one of them quietly drifts.
//
// A nil checker admits: no revocation checker was wired, so revocation is not
// enforced. That posture is chosen at the wiring site, not here.
//
//   - revoked                -> ErrCertificateRevoked (401). The chain
//     verified and the certificate is inside its validity window; the issuer
//     withdrew it.
//   - the lookup failed      -> *RevocationCheckError (503, retryable). FAIL
//     CLOSED: admitting a request whose revocation status is unknown
//     reinstates the exact defect this enforces against, during precisely the
//     degraded conditions an attacker would choose.
//   - no state to read       -> ADMITTED. The only producer is a sealed
//     barrier, where the revocation markers have not been decryptable since
//     boot. Failing closed there would make the barrier UNSEALABLE, since the
//     unseal endpoints are themselves reached over mTLS — an outage with no
//     operator remedy. What such a request can reach is separately confined to
//     the sealed-window allowlist (pkg/api/sealedwindow), which is not the
//     certificate's data plane. This is a stated fail-open, bounded to a
//     window in which the store is not serving data at all.
func EnforceRevocation(ctx context.Context, rc RevocationChecker, peer *x509.Certificate) error {
	if rc == nil {
		return nil
	}
	revoked, err := rc.IsCertRevoked(ctx, peer)
	switch {
	case err != nil && RevocationStateUnavailable(err):
		return nil
	case err != nil:
		return &RevocationCheckError{Err: err}
	case revoked:
		return ErrCertificateRevoked
	}
	return nil
}

// RevocationStateUnavailable reports whether err says revocation state could
// not be consulted at all, rather than that a lookup failed.
//
// The distinction is carried by a MARKER METHOD on the error rather than by a
// sentinel value, so the package that answers revocation questions and the
// package that asks them agree on the distinction without either importing the
// other. pkg/cert.RevocationStateUnavailableError is the implementation.
func RevocationStateUnavailable(err error) bool {
	var unavailable interface{ RevocationStateUnavailable() bool }
	return errors.As(err, &unavailable) && unavailable.RevocationStateUnavailable()
}
