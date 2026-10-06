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

// That the QUIC certs handlers CALL the tenancy gate.
//
// WHAT the gate decides is settled once, against the real authorizer, in
// pkg/auth/middleware/cert_authz_test.go. What a transport has to prove is
// that it delegates -- because every transport here reaches the SAME
// process-wide CertProvider, and a rule enforced on three of four surfaces is
// a rule with a door beside it. That is the failure mode these tests exist to
// catch, and it is invisible from inside the middleware package.
//
// No build constraint: mockCertProvider (mocks_auth_test.go) and newCertServer
// (handlers_certs_test.go) are unconstrained.

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// errCertScopeGateRefused is what the recording authorizer denies with, so a
// test can tell the gate's refusal apart from any other error the handler
// might produce.
var errCertScopeGateRefused = errors.New("quic certs gate: refused by the test authorizer")

// certScopeGateAuthorizer refuses everything and records what it was asked.
// Refusing is the point: a handler that never consults the gate cannot be
// distinguished from one that consults a permissive gate.
type certScopeGateAuthorizer struct {
	asked []string
}

func (a *certScopeGateAuthorizer) Authorize(_ context.Context, _ *auth.Identity, resource, action string) error {
	a.asked = append(a.asked, resource+":"+action)
	return errCertScopeGateRefused
}

// newCertScopeGateServer returns a QUIC server whose certs gate refuses, plus
// the recorder.
func newCertScopeGateServer(cp cert.CertProvider) (*Server, *certScopeGateAuthorizer) {
	authz := &certScopeGateAuthorizer{}
	srv := NewServer(nil, nil, "localhost:0",
		WithCertProvider(cp),
		WithAuthMiddleware(authmw.New(authz)),
	)
	return srv, authz
}

// refusingCertEnroll fails loudly if any of its methods run: every gate test
// that wires it expects the tenancy gate to refuse FIRST.
type refusingCertEnroll struct{ t *testing.T }

func (f *refusingCertEnroll) SignCertificate(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	f.t.Fatal("SignCertificate ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingCertEnroll) EnrollCertificate(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	f.t.Fatal("EnrollCertificate ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingCertEnroll) AddTrustAnchors(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
	f.t.Fatal("AddTrustAnchors ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingCertEnroll) ListTrustAnchors(context.Context, string) ([]trustanchor.Anchor, error) {
	f.t.Fatal("ListTrustAnchors ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingCertEnroll) RemoveTrustAnchor(context.Context, string, string) error {
	f.t.Fatal("RemoveTrustAnchor ran although the tenancy gate refused")
	return nil
}

// newCertEnrollScopeGateServer returns a QUIC server whose certs gate
// refuses, with a CertEnrollmentServicer wired instead of a CertProvider, for
// the IssueCert/EnrollCert/TrustAnchor* handlers.
func newCertEnrollScopeGateServer(t *testing.T) (*Server, *certScopeGateAuthorizer) {
	authz := &certScopeGateAuthorizer{}
	srv := NewServer(nil, nil, "localhost:0",
		WithCertEnrollment(&refusingCertEnroll{t: t}),
		WithAuthMiddleware(authmw.New(authz)),
	)
	return srv, authz
}

// certScopeGateCtx builds the context the authentication path builds.
func certScopeGateCtx() context.Context {
	id := &auth.Identity{Subject: "a-admin", TenantID: "acme", Roles: []string{"admin"}}
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme")
}

// refusingCertProvider fails loudly if a handler reaches it. Every test below
// expects the gate to refuse FIRST, so any call here means the effect ran
// before the decision.
type refusingCertProvider struct {
	mockCertProvider
	t *testing.T
}

func (p *refusingCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	p.t.Fatal("IssueClientCert ran although the tenancy gate refused")
	return nil, nil
}

func (p *refusingCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	p.t.Fatal("RevokeCert ran although the tenancy gate refused")
	return nil, nil
}

func (p *refusingCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	p.t.Fatal("ListCerts ran although the tenancy gate refused")
	return nil, nil
}

func (p *refusingCertProvider) TenantForSerial(_ context.Context, _ string) (string, bool, error) {
	return "globex", true, nil
}

// TestQUICIssueCert_ConsultsTheTenancyGate proves the issuance verb is gated
// on this transport, not only on REST. IssueCert carries no tenant_id of its
// own (the account must exist in the CALLER's tenant), so the gate is always
// asked about the caller's own tenant, "acme".
func TestQUICIssueCert_ConsultsTheTenancyGate(t *testing.T) {
	srv, authz := newCertEnrollScopeGateServer(t)
	payload, _ := json.Marshal(issueCertWireRequest{Username: "a-user", CSRPEM: "csr-pem"})

	_, err := srv.handleIssueCert(certScopeGateCtx(), payload)
	if !errors.Is(err, errCertScopeGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertGateAsked(t, authz, permission.CertsIssue)
}

// TestQUICEnrollCert_ConsultsTheTenancyGate is EnrollCert's twin.
func TestQUICEnrollCert_ConsultsTheTenancyGate(t *testing.T) {
	srv, authz := newCertEnrollScopeGateServer(t)
	payload, _ := json.Marshal(enrollCertWireRequest{Username: "a-user", CertPEM: "leaf-pem"})

	_, err := srv.handleEnrollCert(certScopeGateCtx(), payload)
	if !errors.Is(err, errCertScopeGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertGateAsked(t, authz, permission.CertsEnroll)
}

// TestQUICTrustAnchorAdd_ConsultsTheTenancyGate is the cross-tenant case
// GenerateCert's predecessor used to cover: trust_anchors:add still carries a
// tenant_id field of its own, so this is where a transport could still mint
// against another tenant's trust store if the gate were bypassed.
func TestQUICTrustAnchorAdd_ConsultsTheTenancyGate(t *testing.T) {
	srv, authz := newCertEnrollScopeGateServer(t)
	req := &pb.TrustAnchorAddRequest{TenantId: "globex", CertsPem: "ca-pem"}
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleTrustAnchorAdd(certScopeGateCtx(), payload)
	if !errors.Is(err, errCertScopeGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertGateAsked(t, authz, permission.TrustAnchorsAdd)
}

// TestQUICRevokeCert_ConsultsTheTenancyGate proves the headline verb is gated
// here too. The provider resolves the serial to a foreign tenant, so the gate
// is asked for certs:revoke against THAT tenant.
func TestQUICRevokeCert_ConsultsTheTenancyGate(t *testing.T) {
	srv, authz := newCertScopeGateServer(&refusingCertProvider{t: t})
	payload, _ := json.Marshal(revokeCertWireRequest{Serial: "s-globex"})

	_, err := srv.handleRevokeCert(certScopeGateCtx(), payload)
	if !errors.Is(err, errCertScopeGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertGateAsked(t, authz, permission.CertsRevoke)
}

// TestQUICListCerts_ConsultsTheTenancyGate closes the path by which a serial
// is obtained in the first place.
func TestQUICListCerts_ConsultsTheTenancyGate(t *testing.T) {
	srv, authz := newCertScopeGateServer(&refusingCertProvider{t: t})
	payload, _ := json.Marshal(listCertsWireRequest{TenantID: "globex"})

	_, err := srv.handleListCerts(certScopeGateCtx(), payload)
	if !errors.Is(err, errCertScopeGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertGateAsked(t, authz, permission.CertsList)
}

// TestQUICCertHandlers_NoMiddlewareIsStillServed is the control for the
// unauthenticated posture. Every other handler-level gate in this stack no-ops
// when no middleware is wired, and turning that into a refusal would break
// every embedded and single-node deployment.
func TestQUICCertHandlers_NoMiddlewareIsStillServed(t *testing.T) {
	mock := &mockCertProvider{
		listCertsFn: func(context.Context, string) ([]*cert.CertRecord, error) {
			return []*cert.CertRecord{{Serial: "s1", TenantID: "globex"}}, nil
		},
	}
	srv := newCertServer(mock) // no WithAuthMiddleware
	payload, _ := json.Marshal(listCertsWireRequest{TenantID: "globex"})

	if _, err := srv.handleListCerts(context.Background(), payload); err != nil {
		t.Fatalf("a no-auth deployment must still be served: %v", err)
	}
}

// assertGateAsked checks the recorder saw exactly the permission the handler
// was supposed to submit. Asserting the PERMISSION, not just that something
// was asked, is what keeps a handler from gating on the wrong verb.
func assertGateAsked(t *testing.T, authz *certScopeGateAuthorizer, want permission.Permission) {
	t.Helper()
	if len(authz.asked) != 1 {
		t.Fatalf("gate consulted %d time(s) (%v), want exactly once", len(authz.asked), authz.asked)
	}
	if authz.asked[0] != want.String() {
		t.Errorf("gate asked for %q, want %q", authz.asked[0], want)
	}
}
