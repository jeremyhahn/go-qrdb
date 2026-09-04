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
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
)

// stubRevocationChecker answers from a script and records the certificate it
// was asked about.
type stubRevocationChecker struct {
	revoked bool
	err     error

	calls int
	last  *x509.Certificate
}

func (s *stubRevocationChecker) IsCertRevoked(_ context.Context, peer *x509.Certificate) (bool, error) {
	s.calls++
	s.last = peer
	return s.revoked, s.err
}

// unavailableError is an error carrying the marker method pkg/auth tests for,
// standing in for pkg/cert.RevocationStateUnavailableError without importing
// it (the two packages agree by shape, which is the point of the marker).
type unavailableError struct{}

func (e *unavailableError) Error() string                    { return "revocation state unavailable" }
func (e *unavailableError) RevocationStateUnavailable() bool { return true }

// revocationTestState returns a TLS state carrying one verified client
// certificate for tenant "acme".
func revocationTestState() *tls.ConnectionState {
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(0x2a),
		Subject: pkix.Name{
			CommonName:         "alice",
			OrganizationalUnit: []string{"acme"},
		},
	}
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
}

func TestMTLSAuthenticate_RevokedCertificateIsRejected(t *testing.T) {
	checker := &stubRevocationChecker{revoked: true}
	a := NewMTLSAuthenticator(WithRevocationChecker(checker))
	ctx := WithTLSState(context.Background(), revocationTestState())

	id, err := a.Authenticate(ctx)

	// This is the whole issue: before enforcement existed, a revoked
	// certificate authenticated normally and the operator saw revoked:true.
	if id != nil {
		t.Fatalf("a revoked certificate produced identity %+v", id)
	}
	if !errors.Is(err, ErrCertificateRevoked) {
		t.Fatalf("error = %v, want ErrCertificateRevoked", err)
	}
	if checker.calls != 1 {
		t.Fatalf("revocation checker consulted %d times, want 1", checker.calls)
	}
	if checker.last == nil || checker.last.SerialNumber.Cmp(big.NewInt(0x2a)) != 0 {
		t.Fatal("the checker was not asked about the peer certificate")
	}
}

func TestMTLSAuthenticate_UnrevokedCertificateIsAdmitted(t *testing.T) {
	checker := &stubRevocationChecker{}
	a := NewMTLSAuthenticator(WithRevocationChecker(checker))
	ctx := WithTLSState(context.Background(), revocationTestState())

	id, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "alice" || id.TenantID != "acme" {
		t.Fatalf("identity = %+v, want alice/acme", id)
	}
}

func TestMTLSAuthenticate_LookupFailureFailsClosed(t *testing.T) {
	boom := errors.New("kv read failed")
	checker := &stubRevocationChecker{err: boom}
	a := NewMTLSAuthenticator(WithRevocationChecker(checker))
	ctx := WithTLSState(context.Background(), revocationTestState())

	id, err := a.Authenticate(ctx)

	// Admitting a request whose revocation status is unknown reinstates the
	// exact defect enforcement exists to remove.
	if id != nil {
		t.Fatalf("a certificate of unknown status produced identity %+v", id)
	}
	var checkErr *RevocationCheckError
	if !errors.As(err, &checkErr) {
		t.Fatalf("error = %v (%T), want *RevocationCheckError", err, err)
	}
	if !errors.Is(err, boom) {
		t.Error("the underlying lookup failure did not survive wrapping")
	}
}

func TestMTLSAuthenticate_UnavailableStateIsAdmitted(t *testing.T) {
	checker := &stubRevocationChecker{err: &unavailableError{}}
	a := NewMTLSAuthenticator(WithRevocationChecker(checker))
	ctx := WithTLSState(context.Background(), revocationTestState())

	// The only producer is a sealed barrier. Failing closed there would make
	// the barrier unsealable, since the unseal endpoints are reached over
	// mTLS; what such a request can reach is confined separately.
	id, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate with unavailable revocation state: %v", err)
	}
	if id == nil || id.Subject != "alice" {
		t.Fatalf("identity = %+v, want alice", id)
	}
}

func TestMTLSAuthenticate_NoCheckerDoesNotEnforce(t *testing.T) {
	a := NewMTLSAuthenticator()
	ctx := WithTLSState(context.Background(), revocationTestState())

	id, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "alice" {
		t.Fatalf("identity = %+v, want alice", id)
	}
}

func TestMTLSAuthenticate_TrustDomainConstructorTakesOptions(t *testing.T) {
	// Both constructors funnel through one builder, so an option cannot reach
	// one and miss the other — which would leave every SPIFFE-pinned
	// deployment unenforced.
	checker := &stubRevocationChecker{revoked: true}
	a := NewMTLSAuthenticatorWithTrustDomain("example.org", WithRevocationChecker(checker))
	ctx := WithTLSState(context.Background(), revocationTestState())

	if _, err := a.Authenticate(ctx); !errors.Is(err, ErrCertificateRevoked) {
		t.Fatalf("error = %v, want ErrCertificateRevoked", err)
	}
}

func TestMTLSAuthenticator_NilOptionIsIgnored(t *testing.T) {
	a := NewMTLSAuthenticator(nil)
	if a == nil {
		t.Fatal("NewMTLSAuthenticator(nil) = nil")
	}
}

func TestRevocationStateUnavailable(t *testing.T) {
	if RevocationStateUnavailable(nil) {
		t.Error("RevocationStateUnavailable(nil) = true")
	}
	if RevocationStateUnavailable(errors.New("plain")) {
		t.Error("RevocationStateUnavailable(plain error) = true")
	}
	if !RevocationStateUnavailable(&unavailableError{}) {
		t.Error("RevocationStateUnavailable(marked error) = false")
	}
	// It must survive wrapping, because the checker wraps its cause.
	wrapped := &RevocationCheckError{Err: &unavailableError{}}
	if !RevocationStateUnavailable(wrapped) {
		t.Error("RevocationStateUnavailable(wrapped) = false")
	}
}

func TestRevocationCheckError_Error(t *testing.T) {
	if got := (&RevocationCheckError{}).Error(); got == "" {
		t.Error("Error() on a bare RevocationCheckError is empty")
	}
	e := &RevocationCheckError{Err: errors.New("boom")}
	if got := e.Error(); got == "" || e.Unwrap() == nil {
		t.Errorf("Error() = %q, Unwrap() = %v", got, e.Unwrap())
	}
}
