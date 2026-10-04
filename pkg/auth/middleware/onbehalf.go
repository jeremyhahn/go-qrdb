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

package middleware

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
)

// authSourceMetadataKey / authSourceOnBehalf mark an identity that arrived as
// a forwarded on-behalf assertion rather than from a credential presented on
// this connection. The attested-caller gate reads the mark: the certificate on
// such a connection is the forwarding node's, not the subject's.
const (
	authSourceMetadataKey = "auth_source"
	authSourceOnBehalf    = "on_behalf"
)

// isOnBehalfIdentity reports whether identity was built from a forwarded
// on-behalf assertion.
func isOnBehalfIdentity(identity *auth.Identity) bool {
	return identity != nil && identity.Metadata[authSourceMetadataKey] == authSourceOnBehalf
}

// hasOnBehalfAssertion reports whether the request carries both on-behalf
// headers (assertion + signature). A request carrying only one is treated as
// not present; verification of the pair fails closed if it is malformed.
func hasOnBehalfAssertion(r *http.Request) bool {
	return r.Header.Get(forward.HeaderAssertion) != "" &&
		r.Header.Get(forward.HeaderSignature) != ""
}

// authenticateOnBehalf verifies a signed on-behalf-of assertion presented by a
// cluster peer (a follower forwarding a write to this leader) and returns a
// context carrying the ORIGINAL caller's identity, with roles re-resolved from
// this node's own authorization store.
//
// The flow is fail-closed at every step:
//
//   - The mTLS peer must present a verified certificate; a request without a
//     peer certificate can never carry an honored assertion.
//   - That peer certificate must not be REVOKED. This path bypasses the
//     authenticator chain, so it applies auth.EnforceRevocation itself rather
//     than inheriting the mTLS authenticator's check.
//   - The peer certificate must be a cluster node's (forward.ClusterPeer),
//     checked BEFORE the body is read: a user or service certificate that
//     chains to the cluster CA can never present an honored assertion, so it
//     must not be able to make this node read anything.
//   - The body is read under forward.MaxBodyBytes, the largest body a node
//     forwards, because its digest is needed for the binding check below.
//   - forward.Verify requires that peer certificate to be a cluster-node SPIFFE
//     certificate, verifies the assertion signature against it, and checks the
//     assertion is unexpired and bound to THIS request (method, path, and body
//     digest). A user/service certificate presenting an assertion, a forged or
//     tampered signature, an expired assertion, or a body/path/method swap are
//     all rejected here.
//   - The verified assertion carries IDENTITY ONLY. Roles are re-resolved via
//     finalizeIdentity from the user store -- the asserted identity is never
//     trusted to carry authorization data.
//
// The request body is buffered to compute its digest for binding verification
// and then restored on r so the downstream handler reads the identical bytes.
func (m *AuthMiddleware) authenticateOnBehalf(ctx context.Context, w http.ResponseWriter, r *http.Request) (context.Context, *auth.Identity, error) {
	chains, err := auth.ResolveVerifiedChains(ctx, r.TLS, m.issuerVerifier)
	if err != nil {
		return ctx, nil, err
	}
	peerCert := chains[0][0]

	// A REVOKED forwarding node must not be able to forward. This path never
	// runs the authenticator chain, so it does not inherit the mTLS
	// authenticator's revocation check and has to apply the same policy
	// itself — auth.EnforceRevocation is that policy, defined once.
	if rerr := auth.EnforceRevocation(ctx, m.revocation, peerCert); rerr != nil {
		return ctx, nil, rerr
	}

	// Only a cluster node can present an honored assertion. Refuse anything
	// else before reading a byte of the body, with the same opaque error as
	// every other verification failure below.
	if _, perr := forward.ClusterPeer(peerCert, ""); perr != nil {
		return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "on-behalf assertion rejected"}
	}

	// forward.ClusterPeer only checks the SPIFFE SAN SHAPE (does the cert
	// carry a node-scoped URI SAN?); it says nothing about which CA
	// actually verified the chain. Every tenant's Key B CA is now trusted
	// on this same listener (pkg/clienttrust), so a Key B leaf carrying a
	// forged node SPIFFE SAN would otherwise pass the check above and
	// present an honored assertion AS THE CLUSTER, up to and including
	// acting as system admin. The issuer must be confirmed to be the
	// cluster CA (Key A) here, explicitly, and unknown fails closed
	// (go-qrdb#560 review, B2-1).
	if m.issuerVerifier != nil {
		trusted := false
		for _, c := range chains {
			if len(c) < 2 {
				continue
			}
			if m.issuerVerifier.IsClusterCA(c[1]) {
				trusted = true
				break
			}
		}
		if !trusted {
			return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "on-behalf assertion rejected"}
		}
	}

	body, err := drainAndRestoreBody(w, r, forward.MaxBodyBytes)
	if err != nil {
		return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "cannot read request body for on-behalf binding"}
	}

	assertion, verr := forward.Verify(
		r.Header.Get(forward.HeaderAssertion),
		r.Header.Get(forward.HeaderSignature),
		peerCert,
		"", // trust-domain pin: the issuer check above (when m.issuerVerifier
		// is wired) already confirmed the peer chains to the cluster CA, not
		// merely that TLS verified SOME chain; node-SAN presence + signature
		// binding + node-id match establish cluster-peer trust without a
		// separate pin here.
		time.Now(),
		r.Method,
		r.URL.RequestURI(),
		forward.BodyDigest(body),
	)
	if verr != nil {
		// Collapse every verification failure to a single opaque
		// unauthorized error: never fall back to node-identity auth, and
		// never leak which check failed (reconnaissance oracle).
		return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "on-behalf assertion rejected"}
	}

	// Build the caller identity from the VERIFIED assertion. Roles are left
	// empty here and re-resolved from the authorization store by
	// finalizeIdentity -- asserted roles do not exist and are never trusted.
	identity := &auth.Identity{
		Subject:  assertion.Subject,
		TenantID: assertion.TenantID,
		Metadata: map[string]string{
			authSourceMetadataKey: authSourceOnBehalf,
			"forwarder_nodeid":    assertion.ForwarderNodeID,
		},
	}

	// A caller that authenticated with a holder certificate stays a holder:
	// finalizeIdentity never resolves it to a user account. A profile this
	// node does not recognise is refused rather than dropped, because dropping
	// it would resolve the subject to the account of the same name.
	if assertion.HolderProfile != "" {
		if !holderprofile.Is(assertion.HolderProfile) {
			return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "on-behalf assertion rejected"}
		}
		identity.Metadata[auth.HolderProfileMetadataKey] = assertion.HolderProfile
	}

	// No auth method: the SUBJECT presented nothing here. This is a cluster peer
	// forwarding a call it already authenticated, so the method that let the
	// subject in was observed at that node, not this one. Claiming a method
	// would be inventing one; passing none says so, and the account's
	// permitted-method policy is enforced where the ceremony actually ran.
	return m.finalizeIdentity(ctx, identity, unknownMethod())
}

// verifiedPeerCertificate returns the connection's peer certificate leaf,
// or an auth error when none is present.
//
// Every caller of this function runs strictly AFTER the authenticator
// chain already resolved an identity for this same TLS state via
// auth.ResolveVerifiedChain (cert_binding.go and peer_attestation.go both
// read an already-resolved auth.Identity from context before reaching
// here; authenticateOnBehalf, above, now calls ResolveVerifiedChain
// itself directly rather than this function). So when state.VerifiedChains
// is empty -- which it always is on a connection pkg/clienttrust.Pool.Wire
// touched, see ResolveVerifiedChain's own doc comment -- that emptiness
// was ALREADY resolved once, successfully, by whichever authenticator
// admitted this request; state.PeerCertificates[0] is the identical leaf
// ResolveVerifiedChain would return again, without re-deriving the chain
// a second time for what is a SECONDARY check (cert-binding, peer
// attestation), not a fresh trust decision.
func verifiedPeerCertificate(state *tls.ConnectionState) (*x509.Certificate, error) {
	if state == nil {
		return nil, auth.ErrNoPeerCertificate
	}
	if len(state.VerifiedChains) > 0 && len(state.VerifiedChains[0]) > 0 {
		return state.VerifiedChains[0][0], nil
	}
	if len(state.PeerCertificates) == 0 {
		return nil, auth.ErrNoPeerCertificate
	}
	return state.PeerCertificates[0], nil
}

// drainAndRestoreBody reads the request body under limit and restores it on r
// so the downstream handler can read the identical bytes. It returns the
// buffered body (nil for an empty body), or the *http.MaxBytesError of a body
// over limit, having read at most limit+1 bytes of it.
func drainAndRestoreBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
