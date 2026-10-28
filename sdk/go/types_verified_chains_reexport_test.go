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

package sdk_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// errStubRefused is what stubIssuerVerifier refuses a chain with.
var errStubRefused = errors.New("stub: chain refused")

// stubTrustStateUnavailableError is a refusal made while the verifier could not
// read its trust state.
type stubTrustStateUnavailableError struct{}

func (stubTrustStateUnavailableError) Error() string               { return "stub: trust state unread" }
func (stubTrustStateUnavailableError) TrustStateUnavailable() bool { return true }

// stubIssuerVerifier verifies a leaf to the chain {leaf, issuer} when the
// leaf is trusted, and refuses it with err otherwise.
type stubIssuerVerifier struct {
	trusted *x509.Certificate
	issuer  *x509.Certificate
	err     error
}

var _ sdk.IssuerVerifier = (*stubIssuerVerifier)(nil)

func (s *stubIssuerVerifier) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, _ []*x509.Certificate) ([]*x509.Certificate, error) {
	if leaf != s.trusted {
		return nil, s.err
	}
	return []*x509.Certificate{leaf, s.issuer}, nil
}

func (s *stubIssuerVerifier) IsClusterCA(_ context.Context, issuer *x509.Certificate) bool {
	return issuer == s.issuer
}

func (s *stubIssuerVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return nil
}

// ResolveVerifiedChains verifies a connection with no VerifiedChains through
// the IssuerVerifier, and keeps the chains crypto/tls built when it has them.
func TestResolveVerifiedChains_ResolvesThroughTheVerifier(t *testing.T) {
	leaf, issuer := &x509.Certificate{Raw: []byte("leaf")}, &x509.Certificate{Raw: []byte("issuer")}
	verifier := &stubIssuerVerifier{trusted: leaf, issuer: issuer, err: errStubRefused}

	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	chains, err := sdk.ResolveVerifiedChains(context.Background(), state, verifier)
	if err != nil {
		t.Fatalf("ResolveVerifiedChains: %v", err)
	}
	if len(chains) != 1 || len(chains[0]) != 2 || chains[0][0] != leaf || chains[0][1] != issuer {
		t.Fatalf("chains = %v; want [[leaf issuer]]", chains)
	}

	built := [][]*x509.Certificate{{leaf, issuer}}
	state = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: built}
	chains, err = sdk.ResolveVerifiedChains(context.Background(), state, nil)
	if err != nil || len(chains) != 1 || chains[0][0] != leaf {
		t.Fatalf("ResolveVerifiedChains(crypto/tls chains) = %v, %v; want those chains", chains, err)
	}
}

// ResolveVerifiedChains refuses with a typed error: no certificate, an
// untrusted certificate, no verifier, and a trust state it could not read.
func TestResolveVerifiedChains_TypedRefusals(t *testing.T) {
	ctx := context.Background()
	leaf := &x509.Certificate{Raw: []byte("leaf")}
	untrusted := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}

	for name, tc := range map[string]struct {
		state    *tls.ConnectionState
		verifier sdk.IssuerVerifier
		want     error
	}{
		"no connection state": {nil, &stubIssuerVerifier{err: errStubRefused}, sdk.ErrNoPeerCertificate},
		"no peer certificate": {&tls.ConnectionState{}, &stubIssuerVerifier{err: errStubRefused}, sdk.ErrNoPeerCertificate},
		"untrusted":           {untrusted, &stubIssuerVerifier{err: errStubRefused}, sdk.ErrInvalidClientCertificate},
		"no verifier":         {untrusted, nil, sdk.ErrInvalidClientCertificate},
	} {
		if _, err := sdk.ResolveVerifiedChains(ctx, tc.state, tc.verifier); !errors.Is(err, tc.want) {
			t.Errorf("%s: ResolveVerifiedChains = %v; want %v", name, err, tc.want)
		}
	}

	_, err := sdk.ResolveVerifiedChains(ctx, untrusted, &stubIssuerVerifier{err: stubTrustStateUnavailableError{}})
	var unavailable *sdk.TrustStateUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("ResolveVerifiedChains(trust state unread) = %v; want *TrustStateUnavailableError", err)
	}
}
