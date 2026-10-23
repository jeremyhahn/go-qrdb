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

package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// holderSvc is a BarrierServicer double recording what the holder tools
// hand it. A nil err answers success.
type holderSvc struct {
	service.BarrierServicer
	err     error
	domain  seal.QuorumDomain
	peer    string
	submit  *service.QuorumSubmitRequest
	regUser string
	// soPINs records the SO PIN each rekey or revocation handed the service.
	soPINs []string
	// claims counts the share-claim calls that reached the service.
	claims int
}

func (h *holderSvc) BarrierOfficerRegister(_ context.Context, d seal.QuorumDomain, r *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	h.domain, h.regUser = d, r.Username
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert"), Role: r.Role}, nil
}

func (h *holderSvc) BarrierOfficerList(_ context.Context, d seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	h.domain = d
	if h.err != nil {
		return nil, h.err
	}
	return []*service.BarrierOfficerStatus{{Username: "carol", Role: seal.RoleCustodian}}, nil
}

func (h *holderSvc) BarrierShareClaimBegin(_ context.Context, d seal.QuorumDomain, _ string, peer string) (*service.BarrierShareClaimChallenge, error) {
	h.domain, h.peer = d, peer
	h.claims++
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "ab", ExpiresAt: time.Unix(10, 0)}, nil
}

func (h *holderSvc) BarrierShareClaimComplete(_ context.Context, d seal.QuorumDomain, _, _ string, _ []byte, peer string) (*service.BarrierShareClaimResult, error) {
	h.domain, h.peer = d, peer
	h.claims++
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierShareClaimResult{ShareIndex: 2, Envelope: []byte("env")}, nil
}

func (h *holderSvc) BarrierQuorumBegin(_ context.Context, r *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumChallengeInfo{ChallengeID: "c1", Domain: r.Domain, Op: r.Op, Holder: r.Username}, nil
}

func (h *holderSvc) BarrierQuorumSubmit(_ context.Context, r *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	cp := *r
	h.submit = &cp
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumOutcomeInfo{Domain: r.Domain, Op: r.Op, Required: 3, Submitted: 1}, nil
}

func (h *holderSvc) BarrierRekeyStart(_ context.Context, d seal.QuorumDomain, th int, p seal.FactorPolicy, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumRoundInfo{Domain: d, Op: seal.OpRekey, NewThreshold: th, Policy: p}, nil
}

func (h *holderSvc) BarrierOfficerRevoke(_ context.Context, d seal.QuorumDomain, u string, _ int, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumRoundInfo{Domain: d, Op: seal.OpRekey, Remove: []string{u}}, nil
}

// peerContext binds a verified TLS client certificate into ctx the way the
// MCP transport does, returning its thumbprint.
func peerContext(t *testing.T) (context.Context, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "carol"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}), mtls.Thumbprint(der)
}

// holderTool finds a tool by name.
func holderTool(t *testing.T, tools []serverTool, name string) mcpserver.ToolHandlerFunc {
	t.Helper()
	for _, st := range tools {
		if st.Tool.Name == name {
			return st.Handler
		}
	}
	t.Fatalf("tool %s is not registered", name)
	return nil
}

func TestHolderTools_SystemLifecycle(t *testing.T) {
	ctx, thumb := peerContext(t)
	svc := &holderSvc{}
	tools := barrierHolderTools(svc)
	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := holderTool(t, tools, name)(ctx, buildCallToolRequest(t, name, args))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return resultText(t, res)
	}

	out := call("qrdb_barrier_recovery_officer_register", map[string]any{
		"username": "carol", "role": "custodian", "csr_pem": "csr",
		"factors": `[{"name":"fido2","params":{"public_key":"k"}}]`,
	})
	if svc.domain != seal.DomainRecovery || svc.regUser != "carol" || !strings.Contains(out, `"cert_pem":"cert"`) {
		t.Fatalf("recovery register: domain=%s out=%s", svc.domain, out)
	}
	call("qrdb_barrier_officer_register", map[string]any{"username": "dave", "csr_pem": "csr"})
	if svc.domain != seal.DomainUnseal {
		t.Fatalf("register domain = %s", svc.domain)
	}
	if out := call("qrdb_barrier_officer_list", nil); !strings.Contains(out, `"carol"`) {
		t.Fatalf("list = %s", out)
	}

	for _, name := range []string{"qrdb_barrier_share_claim_begin", "qrdb_barrier_recovery_share_claim_begin"} {
		svc.peer = ""
		if out := call(name, map[string]any{"username": "carol"}); !strings.Contains(out, `"nonce_hex":"ab"`) || svc.peer != thumb {
			t.Fatalf("%s: out=%s peer=%q", name, out, svc.peer)
		}
	}
	for _, name := range []string{"qrdb_barrier_share_claim_complete", "qrdb_barrier_recovery_share_claim_complete"} {
		out := call(name, map[string]any{"username": "carol", "nonce_hex": "ab", "signature": base64.StdEncoding.EncodeToString([]byte("sig"))})
		var resp transport.ShareClaimCompleteResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil || string(resp.Envelope) != "env" || svc.peer != thumb {
			t.Fatalf("%s: out=%s peer=%q", name, out, svc.peer)
		}
	}

	if out := call("qrdb_barrier_quorum_begin", map[string]any{"domain": "unseal", "op": "unseal", "username": "carol"}); !strings.Contains(out, `"challenge_id":"c1"`) {
		t.Fatalf("quorum begin = %s", out)
	}
	share := []byte("share-bytes")
	call("qrdb_barrier_quorum_submit", map[string]any{
		"domain": "unseal", "op": "unseal", "username": "carol", "challenge_id": "c1", "factor": "mtls",
		"response": base64.StdEncoding.EncodeToString([]byte("sig")), "response_metadata": `{"a":"b"}`,
		"share": base64.StdEncoding.EncodeToString(share),
	})
	if svc.submit.PeerCertThumbprint != thumb || svc.submit.ResponseMetadata["a"] != "b" || svc.submit.Op != seal.OpUnseal {
		t.Fatalf("submission = %+v", svc.submit)
	}

	if out := call("qrdb_barrier_rekey_start", map[string]any{"domain": "unseal", "threshold": 3, "policy": "any-enrolled"}); !strings.Contains(out, `"policy":"any-enrolled"`) {
		t.Fatalf("rekey start = %s", out)
	}
	if out := call("qrdb_barrier_officer_revoke", map[string]any{"domain": "unseal", "username": "erin"}); !strings.Contains(out, `"erin"`) {
		t.Fatalf("revoke = %s", out)
	}
}

func TestHolderTools_SystemRefusals(t *testing.T) {
	ctx, _ := peerContext(t)
	svc := &holderSvc{err: ceremony.ErrShareAlreadyClaimed}
	tools := barrierHolderTools(svc)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"qrdb_barrier_officer_register", map[string]any{"username": "u", "csr_pem": "c"}},
		{"qrdb_barrier_officer_list", nil},
		{"qrdb_barrier_share_claim_begin", map[string]any{"username": "u"}},
		{"qrdb_barrier_share_claim_complete", map[string]any{"username": "u"}},
		{"qrdb_barrier_quorum_begin", map[string]any{"op": "unseal"}},
		{"qrdb_barrier_quorum_submit", map[string]any{"op": "unseal"}},
		{"qrdb_barrier_rekey_start", map[string]any{"domain": "unseal", "threshold": 2}},
		{"qrdb_barrier_officer_revoke", map[string]any{"username": "u"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := holderTool(t, tools, tc.name)(ctx, buildCallToolRequest(t, tc.name, tc.args))
			var te *ToolError
			if !errors.As(err, &te) || !errors.Is(err, ceremony.ErrShareAlreadyClaimed) {
				t.Fatalf("err = %v, want the service error in a ToolError", err)
			}
		})
	}

	// Malformed structured arguments are refused before the service.
	for _, name := range []string{"qrdb_barrier_officer_register", "qrdb_barrier_share_claim_begin", "qrdb_barrier_share_claim_complete",
		"qrdb_barrier_quorum_begin", "qrdb_barrier_quorum_submit", "qrdb_barrier_rekey_start", "qrdb_barrier_officer_revoke"} {
		t.Run("malformed/"+name, func(t *testing.T) {
			if _, err := holderTool(t, tools, name)(ctx, buildCallToolRequest(t, name, map[string]any{"factors": "[", "response_metadata": "{", "threshold": "x", "username": 7})); err == nil {
				t.Fatal("malformed arguments were accepted")
			}
		})
	}

	// A claim needs the holder's certificate on the connection.
	for _, name := range []string{"qrdb_barrier_share_claim_begin", "qrdb_barrier_share_claim_complete"} {
		_, err := holderTool(t, tools, name)(context.Background(), buildCallToolRequest(t, name, map[string]any{"username": "u"}))
		if err == nil {
			t.Fatalf("%s without a certificate succeeded", name)
		}
		if code := ServiceErrorCode(err); code != wire.CodeUnauthenticated {
			t.Fatalf("%s without a certificate = %v (code %d), want CodeUnauthenticated", name, err, code)
		}
	}

	// The SO PIN gating rekey and revocation is the barrier service's to
	// verify (behind its lockout): the tool hands it the request's so_pin,
	// and the service's PIN refusal is the tool's.
	pinSvc := &holderSvc{}
	pinned := barrierHolderTools(pinSvc)
	for _, name := range []string{"qrdb_barrier_rekey_start", "qrdb_barrier_officer_revoke"} {
		if _, err := holderTool(t, pinned, name)(ctx, buildCallToolRequest(t, name, map[string]any{"so_pin": "so-pin-1", "username": "u"})); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(pinSvc.soPINs) != 2 || pinSvc.soPINs[0] != "so-pin-1" || pinSvc.soPINs[1] != "so-pin-1" {
		t.Errorf("SO PINs handed to the service = %q", pinSvc.soPINs)
	}
	refusing := barrierHolderTools(&holderSvc{err: pin.ErrPINInvalid})
	for _, name := range []string{"qrdb_barrier_rekey_start", "qrdb_barrier_officer_revoke"} {
		if _, err := holderTool(t, refusing, name)(ctx, buildCallToolRequest(t, name, map[string]any{"so_pin": "wrong", "username": "u"})); !errors.Is(err, pin.ErrPINInvalid) {
			t.Errorf("%s with a wrong SO PIN = %v, want the service's refusal", name, err)
		}
	}
}

// holderTenant is a TenantServicer double for the tenant holder tools.
type holderTenant struct {
	tenant.TenantServicer
	err  error
	peer string
	// claims counts the share-claim calls that reached the service.
	claims int
}

func (h *holderTenant) TenantBarrierOfficerRegister(_ context.Context, _ string, r *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.RegisterOfficerResult{CertPEM: []byte("cert"), Role: r.Role}, nil
}

func (h *holderTenant) TenantBarrierOfficerList(context.Context, string) ([]*ceremony.OfficerStatus, error) {
	if h.err != nil {
		return nil, h.err
	}
	return []*ceremony.OfficerStatus{{Username: "carol"}}, nil
}

func (h *holderTenant) TenantBarrierShareClaimBegin(_ context.Context, _, _, peer string) (*ceremony.ChallengeResponse, error) {
	h.peer = peer
	h.claims++
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.ChallengeResponse{Nonce: []byte{0xab}}, nil
}

func (h *holderTenant) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, peer string) (*ceremony.ShareClaimResult, error) {
	h.peer = peer
	h.claims++
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.ShareClaimResult{ShareIndex: 1, Envelope: []byte("env")}, nil
}

func (h *holderTenant) TenantBarrierQuorumBegin(_ context.Context, _ string, op seal.QuorumOp, u, f string) (*ceremony.QuorumChallenge, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.QuorumChallenge{ChallengeID: "c1", Op: op, Holder: u, Factor: f}, nil
}

func (h *holderTenant) TenantBarrierQuorumSubmit(_ context.Context, _ string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	h.peer = sub.Proof.PeerCertThumbprint
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
}

func (h *holderTenant) TenantBarrierRekeyStart(context.Context, string, int, seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey}, nil
}

func (h *holderTenant) TenantBarrierOfficerRevoke(_ context.Context, _, u string, _ int) (*seal.QuorumRoundInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey, Remove: []string{u}}, nil
}

// tenantHolderArgs are one valid argument set per tenant holder tool.
func tenantHolderArgs(tenantID string) map[string]map[string]any {
	return map[string]map[string]any{
		"qrdb_tenant_barrier_officer_register":     {"tenant_id": tenantID, "username": "carol", "csr_pem": "csr", "role": "custodian"},
		"qrdb_tenant_barrier_officer_list":         {"tenant_id": tenantID},
		"qrdb_tenant_barrier_share_claim_begin":    {"tenant_id": tenantID, "username": "carol"},
		"qrdb_tenant_barrier_share_claim_complete": {"tenant_id": tenantID, "username": "carol", "nonce_hex": "ab"},
		"qrdb_tenant_barrier_quorum_begin":         {"tenant_id": tenantID, "op": "unseal", "username": "carol"},
		"qrdb_tenant_barrier_quorum_submit":        {"tenant_id": tenantID, "op": "unseal", "username": "carol", "share": base64.StdEncoding.EncodeToString([]byte("s"))},
		"qrdb_tenant_barrier_rekey_start":          {"tenant_id": tenantID, "threshold": 2},
		"qrdb_tenant_barrier_officer_revoke":       {"tenant_id": tenantID, "username": "erin"},
	}
}

func TestTenantHolderTools_Lifecycle(t *testing.T) {
	ctx, thumb := peerContext(t)
	svc := &holderTenant{}
	var authorized []permission.Permission
	allow := func(_ context.Context, id string, perm permission.Permission) error {
		if id != "acme" {
			t.Fatalf("authorized tenant %q", id)
		}
		authorized = append(authorized, perm)
		return nil
	}
	tools := tenantHolderTools(svc, allow)
	for name, args := range tenantHolderArgs("acme") {
		res, err := holderTool(t, tools, name)(ctx, buildCallToolRequest(t, name, args))
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
	}
	if svc.peer != thumb {
		t.Fatalf("peer thumbprint = %q, want %q", svc.peer, thumb)
	}
	if len(authorized) != len(tenantHolderArgs("acme")) {
		t.Fatalf("authorized %d calls, want every one", len(authorized))
	}
}

func TestTenantHolderTools_Refusals(t *testing.T) {
	ctx, _ := peerContext(t)
	allow := func(context.Context, string, permission.Permission) error { return nil }

	// A missing tenant is an error result, before authorization.
	tools := tenantHolderTools(&holderTenant{}, func(context.Context, string, permission.Permission) error {
		t.Fatal("authorization reached without a tenant")
		return nil
	})
	for name, args := range tenantHolderArgs("") {
		res, err := holderTool(t, tools, name)(ctx, buildCallToolRequest(t, name, args))
		if err != nil || !res.IsError {
			t.Errorf("%s without a tenant = %+v, %v", name, res, err)
		}
	}

	// A refused authorization is an error result naming it.
	denied := tenantHolderTools(&holderTenant{}, func(context.Context, string, permission.Permission) error { return auth.ErrUnauthorized })
	for name, args := range tenantHolderArgs("acme") {
		res, err := holderTool(t, denied, name)(ctx, buildCallToolRequest(t, name, args))
		if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "authorization denied") {
			t.Errorf("%s denied = %+v, %v", name, res, err)
		}
	}

	// A service failure surfaces as a ToolError.
	failing := tenantHolderTools(&holderTenant{err: tenant.ErrTenantNotFound}, allow)
	for name, args := range tenantHolderArgs("acme") {
		if _, err := holderTool(t, failing, name)(ctx, buildCallToolRequest(t, name, args)); !errors.Is(err, tenant.ErrTenantNotFound) {
			t.Errorf("%s failing = %v", name, err)
		}
	}

	// A claim needs the holder's certificate; malformed arguments are refused.
	ok := tenantHolderTools(&holderTenant{}, allow)
	for _, name := range []string{"qrdb_tenant_barrier_share_claim_begin", "qrdb_tenant_barrier_share_claim_complete"} {
		if _, err := holderTool(t, ok, name)(context.Background(), buildCallToolRequest(t, name, tenantHolderArgs("acme")[name])); err == nil {
			t.Errorf("%s ran without a certificate", name)
		}
	}
	for name := range tenantHolderArgs("acme") {
		if _, err := holderTool(t, ok, name)(ctx, buildCallToolRequest(t, name, map[string]any{"tenant_id": 7})); err == nil {
			t.Errorf("%s accepted a malformed tenant_id", name)
		}
	}
}

// claimCert issues a client certificate for cn, signed by parent, or
// self-signed when parent is nil.
func claimCert(t *testing.T, cn string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: isCA, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// rootsVerifier is the listener's trust pool as auth.IssuerVerifier: it
// verifies a presented chain against roots, as clienttrust.Pool does on a
// listener whose handshake builds no VerifiedChains, and counts its calls.
type rootsVerifier struct {
	roots *x509.CertPool
	calls int
}

var _ auth.IssuerVerifier = (*rootsVerifier)(nil)

func (v *rootsVerifier) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, intermediates []*x509.Certificate) ([]*x509.Certificate, error) {
	v.calls++
	pool := x509.NewCertPool()
	for _, c := range intermediates {
		pool.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{Roots: v.roots, Intermediates: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		return nil, err
	}
	return chains[0], nil
}

func (v *rootsVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v *rootsVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error { return nil }

// claimToolArgs are the share-claim tools of every domain, system and
// tenant, with valid arguments.
func claimToolArgs() map[string]map[string]any {
	complete := map[string]any{"username": "carol", "nonce_hex": "ab", "signature": base64.StdEncoding.EncodeToString([]byte("sig"))}
	tenantArgs := tenantHolderArgs("acme")
	return map[string]map[string]any{
		"qrdb_barrier_share_claim_begin":             {"username": "carol"},
		"qrdb_barrier_share_claim_complete":          complete,
		"qrdb_barrier_recovery_share_claim_begin":    {"username": "carol"},
		"qrdb_barrier_recovery_share_claim_complete": complete,
		"qrdb_tenant_barrier_share_claim_begin":      tenantArgs["qrdb_tenant_barrier_share_claim_begin"],
		"qrdb_tenant_barrier_share_claim_complete":   tenantArgs["qrdb_tenant_barrier_share_claim_complete"],
	}
}

// A share claim binds the holder's certificate, so a certificate the
// connection presented but no one verified must bind nothing: a self-signed
// certificate is refused as ClientCertRequiredError and the claim never
// reaches the service, whether the listener has no verifier or its verifier
// refuses the chain.
func TestHolderClaims_RefuseAnUnverifiedCertificate(t *testing.T) {
	ca, _ := claimCert(t, "ca", true, nil, nil)
	self, _ := claimCert(t, "mallory", false, nil, nil)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	presented := auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{self}})
	allow := func(context.Context, string, permission.Permission) error { return nil }
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"no verifier", presented},
		{"verifier refuses", auth.WithPeerChainVerifier(presented, &rootsVerifier{roots: roots})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, ten := &holderSvc{}, &holderTenant{}
			tools := append(barrierHolderTools(sys), tenantHolderTools(ten, allow)...)
			for name, args := range claimToolArgs() {
				_, err := holderTool(t, tools, name)(tc.ctx, buildCallToolRequest(t, name, args))
				if !errors.As(err, new(*service.ClientCertRequiredError)) {
					t.Errorf("%s = %v, want ClientCertRequiredError", name, err)
				}
				if code := ServiceErrorCode(err); code != wire.CodeUnauthenticated {
					t.Errorf("%s code = %d, want CodeUnauthenticated", name, code)
				}
			}
			if sys.claims != 0 || ten.claims != 0 {
				t.Fatalf("an unverified certificate reached the service: %d system, %d tenant claims", sys.claims, ten.claims)
			}
		})
	}
}

// A verified certificate binds the claim: the service receives the
// thumbprint of the verified chain's leaf, whether crypto/tls built the
// chain or the listener's trust pool verified it.
func TestHolderClaims_BindTheVerifiedLeaf(t *testing.T) {
	ca, caKey := claimCert(t, "ca", true, nil, nil)
	leaf, _ := claimCert(t, "carol", false, ca, caKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	want := mtls.Thumbprint(leaf.Raw)
	allow := func(context.Context, string, permission.Permission) error { return nil }
	pool := &rootsVerifier{roots: roots}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"tls verified chain", auth.WithTLSState(context.Background(), &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{leaf},
			VerifiedChains:   [][]*x509.Certificate{{leaf, ca}},
		})},
		{"trust pool verified", auth.WithPeerChainVerifier(auth.WithTLSState(context.Background(), &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{leaf},
		}), pool)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys, ten := &holderSvc{}, &holderTenant{}
			tools := append(barrierHolderTools(sys), tenantHolderTools(ten, allow)...)
			for name, args := range claimToolArgs() {
				sys.peer, ten.peer = "", ""
				if _, err := holderTool(t, tools, name)(tc.ctx, buildCallToolRequest(t, name, args)); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if got := sys.peer + ten.peer; got != want {
					t.Fatalf("%s bound %q, want the verified leaf's %q", name, got, want)
				}
			}
			if sys.claims+ten.claims != len(claimToolArgs()) {
				t.Fatalf("claims reaching the service = %d, want %d", sys.claims+ten.claims, len(claimToolArgs()))
			}
		})
	}
	if pool.calls != len(claimToolArgs()) {
		t.Fatalf("trust pool verifications = %d, want one per claim", pool.calls)
	}
}
