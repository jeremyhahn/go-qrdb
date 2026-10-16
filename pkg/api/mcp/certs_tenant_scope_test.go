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

// That the MCP certs tools CALL the tenancy gate.
//
// WHAT the gate decides is settled once, against the real authorizer, in
// pkg/auth/middleware/cert_authz_test.go. What a transport has to prove is
// that it delegates -- every transport reaches the SAME process-wide
// CertProvider, so a rule enforced on three of four surfaces is a rule with a
// door beside it.
//
// qrdb_cert_list is the sharpest case on this transport: its tenant_id
// argument DEFAULTS to the system tenant when omitted, so an agent that simply
// left it off read the platform's own inventory.
//
// No build constraint: mockCertProvider (mcp_auth_test.go) is unconstrained.

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// errMCPCertGateRefused is the recording authorizer's refusal, distinguishable
// from every other error a tool handler can produce.
var errMCPCertGateRefused = errors.New("mcp certs gate: refused by the test authorizer")

// mcpCertGateAuthorizer refuses everything and records what it was asked.
// Refusing is the point: a tool that never consults the gate cannot be told
// apart from one consulting a permissive gate.
type mcpCertGateAuthorizer struct {
	asked []string
}

func (a *mcpCertGateAuthorizer) Authorize(_ context.Context, _ *auth.Identity, resource, action string) error {
	a.asked = append(a.asked, resource+":"+action)
	return errMCPCertGateRefused
}

func newMCPCertGate() (*authmw.AuthMiddleware, *mcpCertGateAuthorizer) {
	authz := &mcpCertGateAuthorizer{}
	return authmw.New(authz), authz
}

// mcpCertGateCtx builds the context the authentication path builds.
func mcpCertGateCtx() context.Context {
	id := &auth.Identity{Subject: "a-admin", TenantID: "acme", Roles: []string{"admin"}}
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme")
}

// refusingMCPCertProvider fails loudly if a tool reaches it, so "the effect ran
// before the decision" is a test failure rather than a silent pass.
type refusingMCPCertProvider struct {
	mockCertProvider
	t *testing.T
}

func (p *refusingMCPCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	p.t.Fatal("IssueClientCert ran although the tenancy gate refused")
	return nil, nil
}

func (p *refusingMCPCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	p.t.Fatal("RevokeCert ran although the tenancy gate refused")
	return nil, nil
}

func (p *refusingMCPCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	p.t.Fatal("ListCerts ran although the tenancy gate refused")
	return nil, nil
}

func (p *refusingMCPCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "globex", true, nil
}

// refusingMCPCertEnroll fails loudly if any of its methods run: every gate
// test that wires it expects the tenancy gate to refuse FIRST.
type refusingMCPCertEnroll struct{ t *testing.T }

func (f *refusingMCPCertEnroll) SignCertificate(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	f.t.Fatal("SignCertificate ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingMCPCertEnroll) EnrollCertificate(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	f.t.Fatal("EnrollCertificate ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingMCPCertEnroll) AddTrustAnchors(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
	f.t.Fatal("AddTrustAnchors ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingMCPCertEnroll) ListTrustAnchors(context.Context, string) ([]trustanchor.Anchor, error) {
	f.t.Fatal("ListTrustAnchors ran although the tenancy gate refused")
	return nil, nil
}

func (f *refusingMCPCertEnroll) RemoveTrustAnchor(context.Context, string, string) error {
	f.t.Fatal("RemoveTrustAnchor ran although the tenancy gate refused")
	return nil
}

func (f *refusingMCPCertEnroll) IssueDelegation(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	f.t.Fatal("IssueDelegation ran although the tenancy gate refused")
	return nil, nil
}

// certToolCall builds a CallToolRequest carrying the given arguments.
func certToolCall(name string, args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

// TestMCPCertIssue_ConsultsTheTenancyGate proves the issuance tool is gated
// here. qrdb_cert_issue carries no tenant_id of its own (the account must
// exist in the CALLER's tenant), so the gate is always asked about the
// caller's own tenant, "acme".
func TestMCPCertIssue_ConsultsTheTenancyGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleCertIssue(&refusingMCPCertEnroll{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_cert_issue", map[string]any{
		"username": "a-user",
		"csr_pem":  "csr-pem",
	}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertMCPGateAsked(t, authz, permission.CertsIssue)
}

// TestMCPCertEnroll_ConsultsTheTenancyGate is qrdb_cert_issue's twin.
func TestMCPCertEnroll_ConsultsTheTenancyGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleCertEnroll(&refusingMCPCertEnroll{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_cert_enroll", map[string]any{
		"username": "a-user",
		"cert_pem": "leaf-pem",
	}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertMCPGateAsked(t, authz, permission.CertsEnroll)
}

// TestMCPCertDelegate_ConsultsTheTenancyGate is qrdb_cert_issue's twin for a
// delegation, which likewise names no tenant of its own.
func TestMCPCertDelegate_ConsultsTheTenancyGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleCertDelegate(&refusingMCPCertEnroll{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_cert_delegate", map[string]any{
		"principal": "billing",
		"scope":     []any{"kv:get"},
		"csr_pem":   "csr-pem",
	}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertMCPGateAsked(t, authz, permission.CertsDelegate)
}

// TestMCPTrustAnchorAdd_ConsultsTheTenancyGate is the cross-tenant case
// qrdb_cert_generate used to cover: qrdb_trust_anchor_add still carries a
// tenant_id argument of its own, so this is where this transport could still
// mint against another tenant's trust store if the gate were bypassed.
func TestMCPTrustAnchorAdd_ConsultsTheTenancyGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleTrustAnchorAdd(&refusingMCPCertEnroll{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_trust_anchor_add", map[string]any{
		"tenant_id": "globex",
		"certs_pem": "ca-pem",
	}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertMCPGateAsked(t, authz, permission.TrustAnchorsAdd)
}

// TestMCPCertRevoke_ConsultsTheTenancyGate proves the headline verb is gated.
func TestMCPCertRevoke_ConsultsTheTenancyGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleCertRevoke(&refusingMCPCertProvider{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_cert_revoke", map[string]any{
		"serial": "s-globex",
	}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertMCPGateAsked(t, authz, permission.CertsRevoke)
}

// TestMCPCertList_ConsultsTheTenancyGate closes the enumeration path.
func TestMCPCertList_ConsultsTheTenancyGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleCertList(&refusingMCPCertProvider{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_cert_list", map[string]any{
		"tenant_id": "globex",
	}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertMCPGateAsked(t, authz, permission.CertsList)
}

// TestMCPCertList_OmittedTenantStillReachesTheGate is the case the tool's own
// schema creates. tenant_id defaults to the SYSTEM tenant when absent, so the
// least deliberate call an agent can make is also the most privileged one --
// and it must be gated exactly like an explicit cross-tenant request.
func TestMCPCertList_OmittedTenantStillReachesTheGate(t *testing.T) {
	mw, authz := newMCPCertGate()
	handler := handleCertList(&refusingMCPCertProvider{t: t}, mw)

	_, err := handler(mcpCertGateCtx(), certToolCall("qrdb_cert_list", map[string]any{}))
	if !errors.Is(err, errMCPCertGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal: an omitted tenant_id defaults to the system tenant", err)
	}
	assertMCPGateAsked(t, authz, permission.CertsList)
}

// TestMCPCertTools_NilMiddlewareIsStillServed is the control for the
// unauthenticated posture, which every other handler-level gate in this stack
// shares.
func TestMCPCertTools_NilMiddlewareIsStillServed(t *testing.T) {
	svc := &mockCertProvider{
		listCertsFn: func(context.Context, string) ([]*cert.CertRecord, error) {
			return []*cert.CertRecord{{Serial: "s1", TenantID: "globex"}}, nil
		},
	}
	handler := handleCertList(svc, nil)

	if _, err := handler(context.Background(), certToolCall("qrdb_cert_list", map[string]any{
		"tenant_id": "globex",
	})); err != nil {
		t.Fatalf("a no-auth deployment must still be served: %v", err)
	}
}

// assertMCPGateAsked checks the recorder saw exactly the permission the tool
// was supposed to submit -- not merely that something was asked.
func assertMCPGateAsked(t *testing.T, authz *mcpCertGateAuthorizer, want permission.Permission) {
	t.Helper()
	if len(authz.asked) != 1 {
		t.Fatalf("gate consulted %d time(s) (%v), want exactly once", len(authz.asked), authz.asked)
	}
	if authz.asked[0] != want.String() {
		t.Errorf("gate asked for %q, want %q", authz.asked[0], want)
	}
}
