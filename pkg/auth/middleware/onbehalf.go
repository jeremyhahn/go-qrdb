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
)

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
func (m *AuthMiddleware) authenticateOnBehalf(ctx context.Context, r *http.Request) (context.Context, *auth.Identity, error) {
	peerCert, err := verifiedPeerCertificate(r.TLS)
	if err != nil {
		return ctx, nil, err
	}

	// A REVOKED forwarding node must not be able to forward. This path never
	// runs the authenticator chain, so it does not inherit the mTLS
	// authenticator's revocation check and has to apply the same policy
	// itself — auth.EnforceRevocation is that policy, defined once.
	if rerr := auth.EnforceRevocation(ctx, m.revocation, peerCert); rerr != nil {
		return ctx, nil, rerr
	}

	body, err := drainAndRestoreBody(r)
	if err != nil {
		return ctx, nil, &auth.AuthError{Code: "unauthorized", Message: "cannot read request body for on-behalf binding"}
	}

	assertion, verr := forward.Verify(
		r.Header.Get(forward.HeaderAssertion),
		r.Header.Get(forward.HeaderSignature),
		peerCert,
		"", // trust-domain pin: TLS already verified the peer chains to the
		// cluster CA; node-SAN presence + signature binding + node-id match
		// establish cluster-peer trust without a separate pin here.
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
			"auth_source":      "on_behalf",
			"forwarder_nodeid": assertion.ForwarderNodeID,
		},
	}

	// No auth method: the SUBJECT presented nothing here. This is a cluster peer
	// forwarding a call it already authenticated, so the method that let the
	// subject in was observed at that node, not this one. Claiming a method
	// would be inventing one; passing none says so, and the account's
	// permitted-method policy is enforced where the ceremony actually ran.
	return m.finalizeIdentity(ctx, identity, unknownMethod())
}

// verifiedPeerCertificate returns the leaf of the first verified chain from the
// TLS state, or an auth error when no verified peer certificate is present. A
// forwarded on-behalf assertion is only honored over a mutually-authenticated
// connection whose peer certificate was verified against the cluster CA.
func verifiedPeerCertificate(state *tls.ConnectionState) (*x509.Certificate, error) {
	if state == nil {
		return nil, auth.ErrNoPeerCertificate
	}
	if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		if len(state.PeerCertificates) == 0 {
			return nil, auth.ErrNoPeerCertificate
		}
		return nil, auth.ErrInvalidCertificate
	}
	return state.VerifiedChains[0][0], nil
}

// drainAndRestoreBody reads the full request body and restores it on r so the
// downstream handler can read the identical bytes. It returns the buffered body
// (nil for an empty body).
func drainAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
