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
	"crypto/tls"
)

// Compile-time interface check.
var _ Authenticator = (*MTLSAuthenticator)(nil)

// tlsContextKey is used to extract TLS connection state from context.
type tlsContextKey struct{}

// WithTLSState stores TLS connection state in the context.
func WithTLSState(ctx context.Context, state *tls.ConnectionState) context.Context {
	return context.WithValue(ctx, tlsContextKey{}, state)
}

// TLSStateFromContext retrieves TLS connection state from context.
func TLSStateFromContext(ctx context.Context) *tls.ConnectionState {
	state, _ := ctx.Value(tlsContextKey{}).(*tls.ConnectionState)
	return state
}

// MTLSAuthenticator extracts identity from mTLS peer certificates.
// The Subject CN becomes the identity Subject, and the first OU
// (if present) becomes the TenantID.
type MTLSAuthenticator struct{}

// NewMTLSAuthenticator creates a new mTLS authenticator.
func NewMTLSAuthenticator() *MTLSAuthenticator {
	return &MTLSAuthenticator{}
}

// Name returns "mtls".
func (a *MTLSAuthenticator) Name() string {
	return "mtls"
}

// Authenticate extracts identity from the TLS peer certificate.
// The TLS connection state must be stored in context via WithTLSState.
func (a *MTLSAuthenticator) Authenticate(ctx context.Context) (*Identity, error) {
	state := TLSStateFromContext(ctx)
	if state == nil {
		return nil, ErrNoPeerCertificate
	}

	if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		if len(state.PeerCertificates) == 0 {
			return nil, ErrNoPeerCertificate
		}
		return nil, ErrInvalidCertificate
	}

	cert := state.VerifiedChains[0][0]

	id := &Identity{
		Subject:  cert.Subject.CommonName,
		Metadata: make(map[string]string),
	}

	// First OU becomes TenantID if present.
	if len(cert.Subject.OrganizationalUnit) > 0 {
		id.TenantID = cert.Subject.OrganizationalUnit[0]
	}

	// Organization becomes a metadata field.
	if len(cert.Subject.Organization) > 0 {
		id.Metadata["organization"] = cert.Subject.Organization[0]
	}

	// DNS SANs as metadata.
	if len(cert.DNSNames) > 0 {
		id.Metadata["dns_san"] = cert.DNSNames[0]
	}

	return id, nil
}
