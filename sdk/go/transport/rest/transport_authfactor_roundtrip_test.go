// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// roundTripFactor is a trivial authfactors.Factor used only to exercise a
// REAL server through this SDK's REAL REST client. It performs no
// cryptographic operations.
type roundTripFactor struct{}

func (roundTripFactor) Name() string { return "fake" }

func (roundTripFactor) Enroll(_ context.Context, p *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "roundtrip-" + p.UserID,
		FactorName:   "fake",
		UserID:       p.UserID,
		TenantID:     p.TenantID,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (roundTripFactor) Challenge(_ context.Context, e *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{
		ChallengeID:  "challenge-" + e.EnrollmentID,
		EnrollmentID: e.EnrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(time.Minute).UTC(),
	}, nil
}

func (roundTripFactor) Verify(context.Context, *authfactors.Challenge, *authfactors.Response) error {
	return nil
}

var _ authfactors.Factor = roundTripFactor{}

// roundTripAdminStub answers only Health, which is all pkg/api/rest's
// /health route (and therefore the SDK client's Connect health probe) needs.
// Embedding the nil interface, rather than implementing every AdminServicer
// method, is the same partial-double pattern this repo already uses
// elsewhere (e.g. pkg/server's wiringTestAuthFactorSvc): any OTHER method
// this test never calls panics on the nil embed if it ever is, which is
// preferable to a silent no-op hiding a test that started depending on one.
type roundTripAdminStub struct{ service.AdminServicer }

func (roundTripAdminStub) Health(context.Context) (*service.HealthStatus, error) {
	return &service.HealthStatus{Status: "ok"}, nil
}

// generateRoundTripTLSConfig returns a minimal self-signed server TLS config
// for localhost/127.0.0.1, and a pool holding that certificate for the client
// to trust. The client verifies the server normally against that pool, so the
// test exercises the transport's real TLS path rather than switching
// verification off.
func generateRoundTripTLSConfig(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}, roots
}

// newRoundTripServer starts the REAL pkg/api/rest.Server, bound to a real
// loopback-only port (127.0.0.1:0 -- never a routable address, and no DNS or
// external dependency), over a REAL tenant-routed AuthFactorServicer -- the
// same construction pkg/server's ensureAuthFactorService produces -- and
// returns the pool that trusts its certificate and the base URL an SDK
// transport can reach it at.
//
// This is unavoidable, not a shortcut: pkg/api/rest.Server exposes no
// http.Handler / ServeMux accessor a test could hand to httptest.NewTLSServer
// (the pattern this package's OWN transport_test.go uses for its simpler
// stub backend), and adding one is a public-API change out of scope here.
// Binding a real loopback listener from a plain (untagged) test is the
// established precedent in the server's own package for exactly this
// reason -- see pkg/api/rest/rest_test.go, cors_test.go, http3_bind_test.go
// and rest_embeddable_test.go, none of which carry an integration build tag
// either, and all of which call Server.Start().
//
// No WithAuthMiddleware is supplied, so pkg/api/rest.Server's own middleware
// chain skips authentication entirely (Start only installs the auth wrapper
// when one is configured) and every request resolves the system tenant.
// That is deliberate: this test's subject is the wire CONTRACT between the
// SDK client and the server's handlers -- field names and status codes --
// which tenant isolation (already covered by
// pkg/api/rest/handlers_authfactor_tenant_test.go in the main module) is
// orthogonal to.
func newRoundTripServer(t *testing.T) (roots *x509.CertPool, baseURL string) {
	t.Helper()
	store := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(store, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	resolver, err := tenant.NewStoreResolver(store, nil, tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	authSvc, err := service.NewRoutingAuthFactorService(service.RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{roundTripFactor{}},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}

	serverTLS, roots := generateRoundTripTLSConfig(t)
	srv := restserver.NewServer(nil, roundTripAdminStub{}, "127.0.0.1:0",
		restserver.WithAuthFactorService(authSvc),
		restserver.WithTLS(serverTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return roots, "https://" + srv.Addr()
}

// newRoundTripClient builds the REAL SDK REST transport, trusting only the
// test server's self-signed certificate, and connects it.
func newRoundTripClient(t *testing.T, baseURL string, roots *x509.CertPool) *Transport {
	t.Helper()
	tr, err := New(
		transport.WithAddress(baseURL),
		transport.WithTLSConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestAuthFactorRoundTrip_EnrollChallengeVerifyListRevoke drives a full
// Enroll -> Challenge -> Verify -> List -> Revoke sequence through the REAL
// SDK REST client against the REAL pkg/api/rest.Server, over a REAL
// tenant-routed AuthFactorServicer. It exists because every prior test on
// this seam used either the server's handler DTOs directly or a fake
// responder built to answer whatever the client happened to send, and
// neither shape can catch the client and server disagreeing about the wire
// contract. This test would have failed before the fix on every one of:
// EnrollFactorRequest.Subject marshaled where the server reads "user_id",
// the server's 201 on enroll checked against a client that only accepted
// 200, and the server's 204 on revoke likewise.
func TestAuthFactorRoundTrip_EnrollChallengeVerifyListRevoke(t *testing.T) {
	roots, baseURL := newRoundTripServer(t)
	tr := newRoundTripClient(t, baseURL, roots)
	ctx := context.Background()

	enrollResp, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{
		FactorName: "fake",
		Subject:    "custodian-roundtrip",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if enrollResp.EnrollmentID == "" {
		t.Fatal("EnrollFactor: empty EnrollmentID")
	}
	if enrollResp.UserID != "custodian-roundtrip" {
		t.Fatalf("EnrollFactor: UserID = %q, want custodian-roundtrip", enrollResp.UserID)
	}

	chalResp, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{
		EnrollmentID: enrollResp.EnrollmentID,
	})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if chalResp.ChallengeID == "" {
		t.Fatal("ChallengeFactor: empty ChallengeID")
	}

	if err := tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{
		ChallengeID: chalResp.ChallengeID,
		Value:       []byte("sig"),
	}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}

	listResp, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{Subject: "custodian-roundtrip"})
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if listResp.Total != 1 {
		t.Fatalf("ListFactors: Total = %d, want 1", listResp.Total)
	}

	if err := tr.RevokeFactor(ctx, &transport.RevokeFactorRequest{EnrollmentID: enrollResp.EnrollmentID}); err != nil {
		t.Fatalf("RevokeFactor: %v", err)
	}
}

// TestAuthFactorRoundTrip_EnrollMissingUserIDIsRejected proves the SDK sends
// the field name the server actually reads: an empty Subject reaches the
// server as an empty user_id and is refused with 400, the same way a direct
// caller supplying no user_id would be. Before the field-name fix this
// mismatch was invisible because EVERY enrollment failed at 400 regardless
// of Subject, and no test ever sent a request the server could accept at all.
func TestAuthFactorRoundTrip_EnrollMissingUserIDIsRejected(t *testing.T) {
	roots, baseURL := newRoundTripServer(t)
	tr := newRoundTripClient(t, baseURL, roots)

	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{FactorName: "fake"})
	if err == nil {
		t.Fatal("EnrollFactor with no subject succeeded, want a 400 refusal")
	}
}
