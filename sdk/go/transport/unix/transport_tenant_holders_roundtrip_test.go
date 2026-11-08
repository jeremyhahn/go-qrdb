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

package unix

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"reflect"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// holderTenantSvc is a tenant.TenantServicer that answers the tenant barrier
// holder RPCs and the deleted-tenant and entitlement RPCs the way
// TenantService does: it records the arguments the server handler passed
// and returns either its canned result or err. Anything else panics through
// the nil embedded interface.
type holderTenantSvc struct {
	tenant.TenantServicer

	err error

	tenantID   string
	username   string
	thumbprint string
	nonceHex   string
	signature  []byte
	threshold  int
	op         seal.QuorumOp
	factor     string
	policy     seal.FactorPolicy
	register   *ceremony.RegisterOfficerRequest
	pageToken  string
	pageSize   int
	entitled   *quota.Entitlements
}

var _ tenant.TenantServicer = (*holderTenantSvc)(nil)

func (m *holderTenantSvc) TenantBarrierOfficerRegister(_ context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	m.tenantID, m.register = tenantID, req
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.RegisterOfficerResult{
		CertPEM: []byte("cert-pem"), Role: req.Role, Designated: "mtls",
		EncryptionKEM: "DHKEM(P-256)", Factors: []string{"mtls"},
	}, nil
}

func (m *holderTenantSvc) TenantBarrierOfficerList(_ context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	m.tenantID = tenantID
	if m.err != nil {
		return nil, m.err
	}
	return []*ceremony.OfficerStatus{
		{Username: "carol", Role: seal.RoleCustodian, ShareIndex: 2, Factors: []string{"mtls"}, SharePending: true, EnrolledAt: time.Unix(1700000000, 0)},
	}, nil
}

func (m *holderTenantSvc) TenantBarrierShareClaimBegin(_ context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	m.tenantID, m.username, m.thumbprint = tenantID, username, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.ChallengeResponse{Nonce: []byte{0xca, 0xfe}, Username: username, ExpiresAt: time.Unix(1700000600, 0)}, nil
}

func (m *holderTenantSvc) TenantBarrierShareClaimComplete(_ context.Context, tenantID, username, nonceHex string, signature []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	m.tenantID, m.username, m.nonceHex, m.signature, m.thumbprint = tenantID, username, nonceHex, signature, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.ShareClaimResult{ShareIndex: 3, Envelope: []byte(`{"env":1}`)}, nil
}

func (m *holderTenantSvc) TenantBarrierQuorumBegin(_ context.Context, tenantID string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error) {
	m.tenantID, m.op, m.username, m.factor = tenantID, op, username, factor
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.QuorumChallenge{
		ChallengeID: "ch-1", Domain: seal.DomainUnseal, Op: op, Holder: username, Factor: factor,
		Nonce: []byte("nonce"), MethodData: map[string]string{"k": "v"}, ExpiresAt: time.Unix(1700000900, 0),
	}, nil
}

func (m *holderTenantSvc) TenantBarrierRekeyStart(_ context.Context, tenantID string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	m.tenantID, m.threshold, m.policy = tenantID, threshold, policy
	if m.err != nil {
		return nil, m.err
	}
	return &seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 2, NewThreshold: threshold, Policy: policy, ExpiresAt: time.Unix(1700001000, 0)}, nil
}

func (m *holderTenantSvc) TenantBarrierOfficerRevoke(_ context.Context, tenantID, username string, threshold int) (*seal.QuorumRoundInfo, error) {
	m.tenantID, m.username, m.threshold = tenantID, username, threshold
	if m.err != nil {
		return nil, m.err
	}
	return &seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 2, Submitted: 1, NewThreshold: threshold, Remove: []string{username}}, nil
}

func (m *holderTenantSvc) ListDeletedTenants(_ context.Context, pageToken string, pageSize int) (*tenant.DeletedTenantsPage, error) {
	m.pageToken, m.pageSize = pageToken, pageSize
	if m.err != nil {
		return nil, m.err
	}
	return &tenant.DeletedTenantsPage{
		Tombstones:    []tenant.Tombstone{{ID: "old", Name: "Old Co", VolumePool: "fast", DeletedAt: time.Unix(1700000000, 0)}},
		NextPageToken: "next",
	}, nil
}

func (m *holderTenantSvc) UpdateEntitlements(_ context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	m.tenantID, m.entitled = id, e
	if m.err != nil {
		return nil, m.err
	}
	return &tenant.TenantInfo{ID: id, Name: "Acme", Entitlements: e}, nil
}

func TestUnixRT_TenantBarrierHolders_RegisterAndList(t *testing.T) {
	svc := &holderTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	reg, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{
		TenantID: "acme", Username: "carol", Role: string(seal.RoleCustodian), CSRPEM: "csr-pem",
	})
	if err != nil {
		t.Fatalf("TenantBarrierOfficerRegister: %v", err)
	}
	if svc.tenantID != "acme" || svc.register.Username != "carol" || svc.register.Role != seal.RoleCustodian || string(svc.register.CSRPEM) != "csr-pem" {
		t.Fatalf("service saw tenant %q, request %+v", svc.tenantID, svc.register)
	}
	if reg.CertPEM != "cert-pem" || reg.Role != string(seal.RoleCustodian) || reg.Designated != "mtls" || reg.EncryptionKEM != "DHKEM(P-256)" || !reflect.DeepEqual(reg.Factors, []string{"mtls"}) {
		t.Fatalf("OfficerRegisterResponse = %+v", reg)
	}

	officers, err := tr.TenantBarrierOfficerList(ctx, "acme")
	if err != nil {
		t.Fatalf("TenantBarrierOfficerList: %v", err)
	}
	if len(officers) != 1 {
		t.Fatalf("officers = %+v", officers)
	}
	o := officers[0]
	if o.Username != "carol" || o.Role != string(seal.RoleCustodian) || o.ShareIndex != 2 || !o.SharePending || !o.EnrolledAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("officer = %+v", o)
	}
}

// startMTLSServer is startAuthServer with the client presenting the test
// certificate and the server verifying it: share-claim is identity-gated on
// the verified peer certificate's thumbprint.
func startMTLSServer(t *testing.T, opts ...unixserver.ServerOption) (*Transport, func()) {
	t.Helper()
	socketPath := tempSocketPath(t)
	serverTLS := testUnixServerTLS.Clone()
	serverTLS.ClientAuth = tls.RequireAndVerifyClientCert
	serverTLS.ClientCAs = testUnixServerTLS.RootCAs
	opts = append(opts, unixserver.WithTLS(serverTLS))
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath, opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	tr, err := New(transport.WithAddress(socketPath), transport.WithTLSConfig(&tls.Config{
		Certificates: testUnixServerTLS.Certificates,
		RootCAs:      testUnixServerTLS.RootCAs,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS13,
	}))
	if err != nil {
		srv.Stop()
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		srv.Stop()
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() {
		_ = tr.Close()
		srv.Stop()
	}
}

func TestUnixRT_TenantBarrierHolders_ShareClaim(t *testing.T) {
	svc := &holderTenantSvc{}
	tr, cleanup := startMTLSServer(t, unixserver.WithTenantBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	begin, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
	if err != nil {
		t.Fatalf("TenantBarrierShareClaimBegin: %v", err)
	}
	if svc.tenantID != "acme" || svc.username != "carol" || svc.thumbprint == "" {
		t.Fatalf("service saw tenant %q user %q thumbprint %q", svc.tenantID, svc.username, svc.thumbprint)
	}
	if begin.NonceHex != "cafe" || !begin.ExpiresAt.Equal(time.Unix(1700000600, 0)) {
		t.Fatalf("ShareClaimBeginResponse = %+v", begin)
	}

	done, err := tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{
		TenantID: "acme", Username: "carol", NonceHex: begin.NonceHex, Signature: []byte("sig"),
	})
	if err != nil {
		t.Fatalf("TenantBarrierShareClaimComplete: %v", err)
	}
	if svc.nonceHex != "cafe" || !bytes.Equal(svc.signature, []byte("sig")) || svc.thumbprint == "" {
		t.Fatalf("service saw nonce %q signature %q thumbprint %q", svc.nonceHex, svc.signature, svc.thumbprint)
	}
	if done.ShareIndex != 3 || string(done.Envelope) != `{"env":1}` {
		t.Fatalf("ShareClaimCompleteResponse = %+v", done)
	}
}

func TestUnixRT_TenantBarrierHolders_QuorumRekeyRevoke(t *testing.T) {
	svc := &holderTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	ch, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme", Op: transport.OpUnseal, Username: "carol", Factor: "mtls"})
	if err != nil {
		t.Fatalf("TenantBarrierQuorumBegin: %v", err)
	}
	if svc.op != seal.OpUnseal || svc.username != "carol" || svc.factor != "mtls" {
		t.Fatalf("service saw op %q user %q factor %q", svc.op, svc.username, svc.factor)
	}
	if ch.ChallengeID != "ch-1" || ch.Domain != string(seal.DomainUnseal) || ch.Op != string(seal.OpUnseal) || ch.Username != "carol" ||
		string(ch.Nonce) != "nonce" || ch.MethodData["k"] != "v" || !ch.ExpiresAt.Equal(time.Unix(1700000900, 0)) {
		t.Fatalf("QuorumChallenge = %+v", ch)
	}

	round, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 3, Policy: string(seal.FactorPolicyAnyEnrolled)})
	if err != nil {
		t.Fatalf("TenantBarrierRekeyStart: %v", err)
	}
	if svc.threshold != 3 || svc.policy != seal.FactorPolicyAnyEnrolled {
		t.Fatalf("service saw threshold %d policy %q", svc.threshold, svc.policy)
	}
	if round.Op != string(seal.OpRekey) || round.Required != 2 || round.NewThreshold != 3 || round.Policy != string(seal.FactorPolicyAnyEnrolled) {
		t.Fatalf("rekey QuorumRound = %+v", round)
	}

	round, err = tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "bob", Threshold: 2})
	if err != nil {
		t.Fatalf("TenantBarrierOfficerRevoke: %v", err)
	}
	if svc.username != "bob" || svc.threshold != 2 {
		t.Fatalf("service saw user %q threshold %d", svc.username, svc.threshold)
	}
	if round.Submitted != 1 || !reflect.DeepEqual(round.Remove, []string{"bob"}) {
		t.Fatalf("revoke QuorumRound = %+v", round)
	}
}

// TestUnixRT_TenantBarrierHolders_ErrorsAndNilRequests: a service failure is
// a typed RequestError naming the RPC, and a nil request is refused before
// the wire with the RPC's name and a NilRequestError.
func TestUnixRT_TenantBarrierHolders_ErrorsAndNilRequests(t *testing.T) {
	svc := &holderTenantSvc{err: errors.New("registry unavailable")}
	tr, cleanup := startMTLSServer(t, unixserver.WithTenantBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	failing := map[string]func() error{
		"TenantBarrierOfficerRegister": func() error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"TenantBarrierOfficerList": func() error { _, err := tr.TenantBarrierOfficerList(ctx, "acme"); return err },
		"TenantBarrierShareClaimBegin": func() error {
			_, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"TenantBarrierShareClaimComplete": func() error {
			_, err := tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"TenantBarrierQuorumBegin": func() error {
			_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme", Op: transport.OpUnseal})
			return err
		},
		"TenantBarrierRekeyStart": func() error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 2})
			return err
		},
		"TenantBarrierOfficerRevoke": func() error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "bob"})
			return err
		},
	}
	for method, call := range failing {
		requireUnixRequestError(t, call(), method)
	}

	svc.tenantID = ""
	nilCalls := map[string]func() error{
		"TenantBarrierOfficerRegister":    func() error { _, err := tr.TenantBarrierOfficerRegister(ctx, nil); return err },
		"TenantBarrierShareClaimBegin":    func() error { _, err := tr.TenantBarrierShareClaimBegin(ctx, nil); return err },
		"TenantBarrierShareClaimComplete": func() error { _, err := tr.TenantBarrierShareClaimComplete(ctx, nil); return err },
		"TenantBarrierQuorumBegin":        func() error { _, err := tr.TenantBarrierQuorumBegin(ctx, nil); return err },
		"TenantBarrierRekeyStart":         func() error { _, err := tr.TenantBarrierRekeyStart(ctx, nil); return err },
		"TenantBarrierOfficerRevoke":      func() error { _, err := tr.TenantBarrierOfficerRevoke(ctx, nil); return err },
	}
	for method, call := range nilCalls {
		err := call()
		requireUnixRequestError(t, err, method)
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Fatalf("%s(nil): err = %v, want *NilRequestError", method, err)
		}
	}
	if svc.tenantID != "" {
		t.Fatalf("a nil request reached the service (tenant %q)", svc.tenantID)
	}
}

func TestUnixRT_TenantBarrierHolders_Closed(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTenantBarrierService(&holderTenantSvc{}))
	cleanup()
	ctx := context.Background()

	_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme"})
	assertClosed(t, err)
	_, err = tr.TenantBarrierOfficerList(ctx, "acme")
	assertClosed(t, err)
	_, err = tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme"})
	assertClosed(t, err)
	_, err = tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme"})
	assertClosed(t, err)
	_, err = tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme"})
	assertClosed(t, err)
	_, err = tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme"})
	assertClosed(t, err)
	_, err = tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme"})
	assertClosed(t, err)
	_, err = tr.ListDeletedTenants(ctx, nil)
	assertClosed(t, err)
	_, err = tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"})
	assertClosed(t, err)
}

func TestUnixRT_ListDeletedTenants_PagesThroughTheService(t *testing.T) {
	svc := &holderTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()
	ctx := context.Background()

	resp, err := tr.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: 5, PageToken: "p1"})
	if err != nil {
		t.Fatalf("ListDeletedTenants: %v", err)
	}
	if svc.pageToken != "p1" || svc.pageSize != 5 {
		t.Fatalf("service saw token %q size %d", svc.pageToken, svc.pageSize)
	}
	if resp.NextPageToken != "next" || len(resp.Tenants) != 1 {
		t.Fatalf("ListDeletedTenantsResponse = %+v", resp)
	}
	if d := resp.Tenants[0]; d.ID != "old" || d.Name != "Old Co" || d.VolumePool != "fast" || !d.DeletedAt.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("deleted tenant = %+v", d)
	}

	// A nil request is the first page at the server's default size.
	if _, err := tr.ListDeletedTenants(ctx, nil); err != nil {
		t.Fatalf("ListDeletedTenants(nil): %v", err)
	}
	if svc.pageToken != "" || svc.pageSize != 0 {
		t.Fatalf("nil request reached the service as token %q size %d", svc.pageToken, svc.pageSize)
	}

	svc.err = errors.New("tombstones unreadable")
	_, err = tr.ListDeletedTenants(ctx, nil)
	requireUnixRequestError(t, err, "ListDeletedTenants")
}

func TestUnixRT_UpdateTenantEntitlements_ForwardsEveryField(t *testing.T) {
	svc := &holderTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()
	ctx := context.Background()

	want := transport.TenantEntitlements{
		AllowedIsolationModes: []string{"prefix", "volume"}, AllowDedicatedRaft: true, AllowVolumeStorage: true,
		AllowSystemManagedBarrier: true, AllowedBarrierModes: []string{"shamir"}, AllowDedicatedWAL: true,
		AllowedVolumePools: []string{"fast", "bulk"},
	}
	resp, err := tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: "acme", Entitlements: want})
	if err != nil {
		t.Fatalf("UpdateTenantEntitlements: %v", err)
	}
	got := svc.entitled
	if svc.tenantID != "acme" || got == nil ||
		!reflect.DeepEqual(got.AllowedIsolationModes, want.AllowedIsolationModes) || !got.AllowDedicatedRaft || !got.AllowVolumeStorage ||
		!got.AllowSystemManagedBarrier || !reflect.DeepEqual(got.AllowedBarrierModes, want.AllowedBarrierModes) || !got.AllowDedicatedWAL ||
		!reflect.DeepEqual(got.AllowedVolumePools, want.AllowedVolumePools) {
		t.Fatalf("service saw tenant %q entitlements %+v", svc.tenantID, got)
	}
	if resp.Tenant.ID != "acme" || resp.Tenant.Name != "Acme" {
		t.Fatalf("UpdateTenantEntitlementsResponse = %+v", resp)
	}

	svc.err = errors.New("tenant locked")
	_, err = tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: "acme", Entitlements: want})
	requireUnixRequestError(t, err, "UpdateTenantEntitlements")
}

func TestUnixRT_UpdateTenantEntitlements_NilRequest(t *testing.T) {
	svc := &holderTenantSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	defer cleanup()

	_, err := tr.UpdateTenantEntitlements(context.Background(), nil)
	requireUnixRequestError(t, err, "UpdateTenantEntitlements")
	var nre *transport.NilRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("UpdateTenantEntitlements(nil) = %v, want NilRequestError", err)
	}
	if svc.entitled != nil || svc.tenantID != "" {
		t.Fatalf("a nil request reached the service: tenant %q", svc.tenantID)
	}
}
