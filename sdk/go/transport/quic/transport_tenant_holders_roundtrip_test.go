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

package quic

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// quicTenantHolderSvc is a tenant.TenantServicer that answers the tenant
// barrier holder RPCs and the deleted-tenant and entitlement RPCs the way
// TenantService does: it records the arguments the server handler passed
// and returns either its canned result or err. Anything else panics through
// the nil embedded interface.
type quicTenantHolderSvc struct {
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
	submission *seal.HolderSubmission
	pageToken  string
	pageSize   int
	entitled   *quota.Entitlements
}

var _ tenant.TenantServicer = (*quicTenantHolderSvc)(nil)

func (m *quicTenantHolderSvc) TenantBarrierOfficerRegister(_ context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	m.tenantID, m.register = tenantID, req
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.RegisterOfficerResult{
		CertPEM: []byte("cert-pem"), Role: req.Role, Designated: "mtls",
		EncryptionKEM: "DHKEM(P-256)", Factors: []string{"mtls"},
	}, nil
}

func (m *quicTenantHolderSvc) TenantBarrierOfficerList(_ context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	m.tenantID = tenantID
	if m.err != nil {
		return nil, m.err
	}
	return []*ceremony.OfficerStatus{
		{Username: "carol", Role: seal.RoleCustodian, ShareIndex: 2, Factors: []string{"mtls"}, SharePending: true, EnrolledAt: time.Unix(1700000000, 0)},
	}, nil
}

func (m *quicTenantHolderSvc) TenantBarrierShareClaimBegin(_ context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	m.tenantID, m.username, m.thumbprint = tenantID, username, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.ChallengeResponse{Nonce: []byte{0xca, 0xfe}, Username: username, ExpiresAt: time.Unix(1700000600, 0)}, nil
}

func (m *quicTenantHolderSvc) TenantBarrierShareClaimComplete(_ context.Context, tenantID, username, nonceHex string, signature []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	m.tenantID, m.username, m.nonceHex, m.signature, m.thumbprint = tenantID, username, nonceHex, signature, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.ShareClaimResult{ShareIndex: 3, Envelope: []byte(`{"env":1}`)}, nil
}

func (m *quicTenantHolderSvc) TenantBarrierQuorumBegin(_ context.Context, tenantID string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error) {
	m.tenantID, m.op, m.username, m.factor = tenantID, op, username, factor
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.QuorumChallenge{
		ChallengeID: "ch-1", Domain: seal.DomainUnseal, Op: op, Holder: username, Factor: factor,
		Nonce: []byte("nonce"), MethodData: map[string]string{"k": "v"}, ExpiresAt: time.Unix(1700000900, 0),
	}, nil
}

// TenantBarrierQuorumSubmit records the submission without its share: the
// server handler wipes the share once this returns.
func (m *quicTenantHolderSvc) TenantBarrierQuorumSubmit(_ context.Context, tenantID string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	m.tenantID = tenantID
	m.submission = &seal.HolderSubmission{Domain: sub.Domain, Op: sub.Op, Holder: sub.Holder, Share: bytes.Clone(sub.Share)}
	if m.err != nil {
		return nil, m.err
	}
	return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
}

func (m *quicTenantHolderSvc) TenantBarrierRekeyStart(_ context.Context, tenantID string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	m.tenantID, m.threshold, m.policy = tenantID, threshold, policy
	if m.err != nil {
		return nil, m.err
	}
	return &seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 2, NewThreshold: threshold, Policy: policy, ExpiresAt: time.Unix(1700001000, 0)}, nil
}

func (m *quicTenantHolderSvc) TenantBarrierOfficerRevoke(_ context.Context, tenantID, username string, threshold int) (*seal.QuorumRoundInfo, error) {
	m.tenantID, m.username, m.threshold = tenantID, username, threshold
	if m.err != nil {
		return nil, m.err
	}
	return &seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 2, Submitted: 1, NewThreshold: threshold, Remove: []string{username}}, nil
}

func (m *quicTenantHolderSvc) ListDeletedTenants(_ context.Context, pageToken string, pageSize int) (*tenant.DeletedTenantsPage, error) {
	m.pageToken, m.pageSize = pageToken, pageSize
	if m.err != nil {
		return nil, m.err
	}
	return &tenant.DeletedTenantsPage{
		Tombstones:    []tenant.Tombstone{{ID: "old", Name: "Old Co", VolumePool: "fast", DeletedAt: time.Unix(1700000000, 0)}},
		NextPageToken: "next",
	}, nil
}

func (m *quicTenantHolderSvc) UpdateEntitlements(_ context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	m.tenantID, m.entitled = id, e
	if m.err != nil {
		return nil, m.err
	}
	return &tenant.TenantInfo{ID: id, Name: "Acme", Entitlements: e}, nil
}

// requireQUICNilTenantHolderRequest asserts err is the RequestError naming
// method that a tenant holder method refuses a nil request with.
func requireQUICNilTenantHolderRequest(t *testing.T, err error, method string) {
	t.Helper()
	requireQUICRequestError(t, err, method)
	var nre *nilTenantBarrierRequestError
	if !errors.As(err, &nre) || nre.Method != method {
		t.Fatalf("%s(nil): err = %v, want *nilTenantBarrierRequestError", method, err)
	}
}

func TestQUICRoundTrip_TenantBarrierHolders_RegisterAndList(t *testing.T) {
	svc := &quicTenantHolderSvc{}
	tr := startRoundTripServer(t, quicserver.WithTenantService(svc))
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

func TestQUICRoundTrip_TenantBarrierHolders_ShareClaim(t *testing.T) {
	svc := &quicTenantHolderSvc{}
	tr := startMTLSRoundTripServer(t, quicserver.WithTenantService(svc))
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

func TestQUICRoundTrip_TenantBarrierHolders_QuorumRekeyRevoke(t *testing.T) {
	svc := &quicTenantHolderSvc{}
	tr := startRoundTripServer(t, quicserver.WithTenantService(svc))
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

	out, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{
		TenantID: "acme", Op: transport.OpUnseal, Username: "carol", ChallengeID: "ch-1",
		Factor: "mtls", Response: []byte("resp"), Share: []byte("share"),
	})
	if err != nil {
		t.Fatalf("TenantBarrierQuorumSubmit: %v", err)
	}
	if sub := svc.submission; svc.tenantID != "acme" || sub == nil || sub.Holder != "carol" || sub.Op != seal.OpUnseal || string(sub.Share) != "share" {
		t.Fatalf("service saw tenant %q submission %+v", svc.tenantID, svc.submission)
	}
	if out.Op != string(seal.OpUnseal) || out.Required != 2 || out.Submitted != 1 || out.Complete {
		t.Fatalf("QuorumOutcome = %+v", out)
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

// TestQUICRoundTrip_TenantBarrierHolders_ErrorsAndNilRequests: a service
// failure is a typed RequestError naming the RPC, and a nil request is
// refused before the wire with the RPC's name and a NilRequestError.
func TestQUICRoundTrip_TenantBarrierHolders_ErrorsAndNilRequests(t *testing.T) {
	svc := &quicTenantHolderSvc{err: errors.New("registry unavailable")}
	tr := startMTLSRoundTripServer(t, quicserver.WithTenantService(svc))
	ctx := context.Background()

	failing := map[string]func() error{
		"tenant.BarrierOfficerRegister": func() error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"tenant.BarrierOfficerList": func() error { _, err := tr.TenantBarrierOfficerList(ctx, "acme"); return err },
		"tenant.BarrierShareClaimBegin": func() error {
			_, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"tenant.BarrierShareClaimComplete": func() error {
			_, err := tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		"tenant.BarrierQuorumBegin": func() error {
			_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme", Op: transport.OpUnseal})
			return err
		},
		"tenant.BarrierQuorumSubmit": func() error {
			_, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: "acme", Op: transport.OpUnseal, Username: "carol"})
			return err
		},
		"tenant.BarrierRekeyStart": func() error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 2})
			return err
		},
		"tenant.BarrierOfficerRevoke": func() error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "bob"})
			return err
		},
	}
	for method, call := range failing {
		requireQUICRequestError(t, call(), method)
	}

	svc.tenantID = ""
	nilCalls := map[string]func() error{
		"TenantBarrierOfficerRegister":    func() error { _, err := tr.TenantBarrierOfficerRegister(ctx, nil); return err },
		"TenantBarrierShareClaimBegin":    func() error { _, err := tr.TenantBarrierShareClaimBegin(ctx, nil); return err },
		"TenantBarrierShareClaimComplete": func() error { _, err := tr.TenantBarrierShareClaimComplete(ctx, nil); return err },
		"TenantBarrierQuorumBegin":        func() error { _, err := tr.TenantBarrierQuorumBegin(ctx, nil); return err },
		"TenantBarrierQuorumSubmit":       func() error { _, err := tr.TenantBarrierQuorumSubmit(ctx, nil); return err },
		"TenantBarrierRekeyStart":         func() error { _, err := tr.TenantBarrierRekeyStart(ctx, nil); return err },
		"TenantBarrierOfficerRevoke":      func() error { _, err := tr.TenantBarrierOfficerRevoke(ctx, nil); return err },
	}
	for method, call := range nilCalls {
		requireQUICNilTenantHolderRequest(t, call(), method)
	}
	if svc.tenantID != "" {
		t.Fatalf("a nil request reached the service (tenant %q)", svc.tenantID)
	}
}

func TestQUICRoundTrip_TenantBarrierHolders_Closed(t *testing.T) {
	tr := startRoundTripServer(t, quicserver.WithTenantService(&quicTenantHolderSvc{}))
	_ = tr.Close()
	ctx := context.Background()

	calls := map[string]func() error{
		"TenantBarrierOfficerRegister": func() error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierOfficerList": func() error { _, err := tr.TenantBarrierOfficerList(ctx, "acme"); return err },
		"TenantBarrierShareClaimBegin": func() error {
			_, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierShareClaimComplete": func() error {
			_, err := tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierQuorumBegin": func() error {
			_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierQuorumSubmit": func() error {
			_, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierRekeyStart": func() error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme"})
			return err
		},
		"TenantBarrierOfficerRevoke": func() error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme"})
			return err
		},
		"ListDeletedTenants": func() error { _, err := tr.ListDeletedTenants(ctx, nil); return err },
		"UpdateTenantEntitlements": func() error {
			_, err := tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"})
			return err
		},
	}
	for method, call := range calls {
		var ce *transport.ClosedError
		if err := call(); !errors.As(err, &ce) {
			t.Fatalf("%s after Close: err = %v, want *ClosedError", method, err)
		}
	}
}

func TestQUICRoundTrip_ListDeletedTenants_PagesThroughTheService(t *testing.T) {
	svc := &quicTenantHolderSvc{}
	tr := startRoundTripServer(t, quicserver.WithTenantService(svc))
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
	requireQUICRequestError(t, err, "ListDeletedTenants")
}

func TestQUICRoundTrip_UpdateTenantEntitlements_ForwardsEveryField(t *testing.T) {
	svc := &quicTenantHolderSvc{}
	tr := startRoundTripServer(t, quicserver.WithTenantService(svc))
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
	requireQUICRequestError(t, err, "UpdateTenantEntitlements")
}

func TestQUICRoundTrip_UpdateTenantEntitlements_NilRequest(t *testing.T) {
	svc := &quicTenantHolderSvc{}
	tr := startRoundTripServer(t, quicserver.WithTenantService(svc))

	_, err := tr.UpdateTenantEntitlements(context.Background(), nil)
	requireQUICRequestError(t, err, "UpdateTenantEntitlements")
	var nre *transport.NilRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("UpdateTenantEntitlements(nil): err = %v, want *NilRequestError", err)
	}
	if svc.entitled != nil || svc.tenantID != "" {
		t.Fatalf("a nil request reached the service: tenant %q", svc.tenantID)
	}
}
