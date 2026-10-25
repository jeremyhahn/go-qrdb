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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// holderBarrier is the barrier double grown the share-holder verbs. Each
// verb records the domain and the SO PIN it received, and refuses with err
// when it is set.
type holderBarrier struct {
	*mockBarrierSvc
	err     error
	domains []seal.QuorumDomain
	soPINs  []string
	submit  *service.QuorumSubmitRequest
}

func (h *holderBarrier) BarrierOfficerRegister(_ context.Context, d seal.QuorumDomain, r *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	h.domains = append(h.domains, d)
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert-" + r.Username), Role: r.Role}, nil
}

func (h *holderBarrier) BarrierOfficerList(_ context.Context, d seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	h.domains = append(h.domains, d)
	if h.err != nil {
		return nil, h.err
	}
	return []*service.BarrierOfficerStatus{{Username: "carol", Role: seal.RoleCustodian}}, nil
}

func (h *holderBarrier) BarrierShareClaimBegin(context.Context, seal.QuorumDomain, string, string) (*service.BarrierShareClaimChallenge, error) {
	return nil, errors.New("holderBarrier: a share claim reached the service without a holder certificate")
}

func (h *holderBarrier) BarrierShareClaimComplete(context.Context, seal.QuorumDomain, string, string, []byte, string) (*service.BarrierShareClaimResult, error) {
	return nil, errors.New("holderBarrier: a share claim reached the service without a holder certificate")
}

func (h *holderBarrier) BarrierQuorumBegin(_ context.Context, r *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumChallengeInfo{ChallengeID: "c1", Domain: r.Domain, Op: r.Op, Holder: r.Username}, nil
}

func (h *holderBarrier) BarrierQuorumSubmit(_ context.Context, r *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	// The server wipes the share once the submission returns, so the
	// double keeps its own copy.
	cp := *r
	cp.Share = append([]byte(nil), r.Share...)
	h.submit = &cp
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumOutcomeInfo{Domain: r.Domain, Op: r.Op, Required: 3, Submitted: 1}, nil
}

func (h *holderBarrier) BarrierRekeyStart(_ context.Context, d seal.QuorumDomain, th int, p seal.FactorPolicy, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumRoundInfo{Domain: d, Op: seal.OpRekey, NewThreshold: th, Policy: p}, nil
}

func (h *holderBarrier) BarrierOfficerRevoke(_ context.Context, d seal.QuorumDomain, u string, _ int, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumRoundInfo{Domain: d, Op: seal.OpRekey, Remove: []string{u}}, nil
}

func TestMCP_BarrierHolders_RoundTrip(t *testing.T) {
	svc := &holderBarrier{mockBarrierSvc: newMockBarrierSvc()}
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()
	ctx := context.Background()

	reg, err := tr.OfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "carol", Role: "custodian", CSRPEM: "csr"})
	if err != nil {
		t.Fatalf("OfficerRegister: %v", err)
	}
	if reg.CertPEM != "cert-carol" || reg.Role != "custodian" {
		t.Fatalf("OfficerRegister = %+v", reg)
	}
	if _, err := tr.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "dave", CSRPEM: "csr"}); err != nil {
		t.Fatalf("RecoveryOfficerRegister: %v", err)
	}
	officers, err := tr.OfficerList(ctx)
	if err != nil {
		t.Fatalf("OfficerList: %v", err)
	}
	if len(officers) != 1 || officers[0].Username != "carol" {
		t.Fatalf("OfficerList = %+v", officers)
	}
	if len(svc.domains) != 3 || svc.domains[0] != seal.DomainUnseal || svc.domains[1] != seal.DomainRecovery {
		t.Fatalf("domains = %v, want unseal, recovery, unseal", svc.domains)
	}

	ch, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "carol"})
	if err != nil {
		t.Fatalf("BarrierQuorumBegin: %v", err)
	}
	if ch.ChallengeID != "c1" || ch.Username != "carol" {
		t.Fatalf("BarrierQuorumBegin = %+v", ch)
	}
	out, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{
		Domain: "unseal", Op: "unseal", Username: "carol", ChallengeID: "c1", Factor: "mtls",
		Response: []byte("sig"), Share: []byte("share"),
	})
	if err != nil {
		t.Fatalf("BarrierQuorumSubmit: %v", err)
	}
	if out.Required != 3 || out.Submitted != 1 || svc.submit == nil || string(svc.submit.Share) != "share" {
		t.Fatalf("BarrierQuorumSubmit = %+v, service saw %+v", out, svc.submit)
	}

	round, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: "unseal", Threshold: 3, Policy: "any-enrolled", SOPin: "so-pin-1"})
	if err != nil {
		t.Fatalf("BarrierRekeyStart: %v", err)
	}
	if round.NewThreshold != 3 || round.Policy != "any-enrolled" {
		t.Fatalf("BarrierRekeyStart = %+v", round)
	}
	rev, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "erin", SOPin: "so-pin-2"})
	if err != nil {
		t.Fatalf("BarrierOfficerRevoke: %v", err)
	}
	if len(rev.Remove) != 1 || rev.Remove[0] != "erin" {
		t.Fatalf("BarrierOfficerRevoke = %+v", rev)
	}
	if len(svc.soPINs) != 2 || svc.soPINs[0] != "so-pin-1" || svc.soPINs[1] != "so-pin-2" {
		t.Fatalf("SO PINs = %q", svc.soPINs)
	}
}

// TestMCP_BarrierHolders_ShareClaimNeedsHolderCertificate pins that an
// in-process caller, which has no TLS peer, cannot claim a share: the claim
// is identity-gated on the holder's certificate and is refused before the
// service is reached.
func TestMCP_BarrierHolders_ShareClaimNeedsHolderCertificate(t *testing.T) {
	tr, cleanup := connectBarrierInProcess(t, &holderBarrier{mockBarrierSvc: newMockBarrierSvc()})
	defer cleanup()
	ctx := context.Background()

	begin := &transport.ShareClaimBeginRequest{Username: "carol"}
	complete := &transport.ShareClaimCompleteRequest{Username: "carol"}
	_, err := tr.ShareClaimBegin(ctx, begin)
	requireRequestError(t, err, "ShareClaimBegin")
	_, err = tr.ShareClaimComplete(ctx, complete)
	requireRequestError(t, err, "ShareClaimComplete")
	_, err = tr.RecoveryShareClaimBegin(ctx, begin)
	requireRequestError(t, err, "RecoveryShareClaimBegin")
	_, err = tr.RecoveryShareClaimComplete(ctx, complete)
	requireRequestError(t, err, "RecoveryShareClaimComplete")
}

func TestMCP_BarrierHolders_RefusalsAndClosed(t *testing.T) {
	tr, cleanup := connectBarrierInProcess(t, &holderBarrier{mockBarrierSvc: newMockBarrierSvc(), err: errors.New("refused")})
	defer cleanup()
	ctx := context.Background()

	calls := map[string]func() error{
		"OfficerRegister": func() error {
			_, err := tr.OfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "u", CSRPEM: "c"})
			return err
		},
		"RecoveryOfficerRegister": func() error {
			_, err := tr.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "u", CSRPEM: "c"})
			return err
		},
		"OfficerList": func() error { _, err := tr.OfficerList(ctx); return err },
		"BarrierQuorumBegin": func() error {
			_, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "u"})
			return err
		},
		"BarrierQuorumSubmit": func() error {
			_, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "u", Share: []byte("s")})
			return err
		},
		"BarrierRekeyStart": func() error {
			_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: "unseal", Threshold: 2, SOPin: "p"})
			return err
		},
		"BarrierOfficerRevoke": func() error {
			_, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "u", SOPin: "p"})
			return err
		},
	}
	for name, call := range calls {
		requireRequestError(t, call(), name)
	}

	// With no SO PIN in the request and none bound to the context, the
	// rekey and revoke are refused before anything is sent.
	if _, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: "unseal", Threshold: 2}); err == nil {
		t.Error("BarrierRekeyStart without an SO PIN succeeded")
	}
	if _, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "u"}); err == nil {
		t.Error("BarrierOfficerRevoke without an SO PIN succeeded")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, call := range calls {
		requireClosed(t, call())
	}
	_, err := tr.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "u"})
	requireClosed(t, err)
}

// holderTenant is the tenant double grown the per-tenant share-holder verbs
// and the deleted-tenant listing and reconcile. It records the tenant each
// verb was addressed to, and refuses with err when it is set.
type holderTenant struct {
	*mockTenantSvc
	err       error
	tenants   []string
	pageToken string
	pageSize  int
}

func (h *holderTenant) seen(tenantID string) error {
	h.tenants = append(h.tenants, tenantID)
	return h.err
}

func (h *holderTenant) TenantBarrierOfficerRegister(_ context.Context, tenantID string, r *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if err := h.seen(tenantID); err != nil {
		return nil, err
	}
	return &ceremony.RegisterOfficerResult{CertPEM: []byte("cert-" + r.Username), Role: r.Role}, nil
}

func (h *holderTenant) TenantBarrierOfficerList(_ context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if err := h.seen(tenantID); err != nil {
		return nil, err
	}
	return []*ceremony.OfficerStatus{{Username: "carol"}}, nil
}

func (h *holderTenant) TenantBarrierQuorumBegin(_ context.Context, tenantID string, op seal.QuorumOp, u, f string) (*ceremony.QuorumChallenge, error) {
	if err := h.seen(tenantID); err != nil {
		return nil, err
	}
	return &ceremony.QuorumChallenge{ChallengeID: "c1", Op: op, Holder: u, Factor: f}, nil
}

func (h *holderTenant) TenantBarrierQuorumSubmit(_ context.Context, tenantID string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	if err := h.seen(tenantID); err != nil {
		return nil, err
	}
	return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
}

func (h *holderTenant) TenantBarrierRekeyStart(_ context.Context, tenantID string, th int, p seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	if err := h.seen(tenantID); err != nil {
		return nil, err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey, NewThreshold: th, Policy: p}, nil
}

func (h *holderTenant) TenantBarrierOfficerRevoke(_ context.Context, tenantID, u string, _ int) (*seal.QuorumRoundInfo, error) {
	if err := h.seen(tenantID); err != nil {
		return nil, err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey, Remove: []string{u}}, nil
}

func (h *holderTenant) ListDeletedTenants(_ context.Context, pageToken string, pageSize int) (*tenant.DeletedTenantsPage, error) {
	h.pageToken, h.pageSize = pageToken, pageSize
	if h.err != nil {
		return nil, h.err
	}
	return &tenant.DeletedTenantsPage{Tombstones: []tenant.Tombstone{{ID: "gone", Name: "Gone Inc"}}, NextPageToken: "next"}, nil
}

func (h *holderTenant) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &tenant.ReconcileReport{Inspected: 4}, nil
}

func TestMCP_TenantHolders_RoundTrip(t *testing.T) {
	svc := &holderTenant{mockTenantSvc: newMockTenantSvc()}
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()
	ctx := context.Background()

	reg, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme", Username: "carol", Role: "custodian", CSRPEM: "csr"})
	if err != nil {
		t.Fatalf("TenantBarrierOfficerRegister: %v", err)
	}
	if reg.CertPEM != "cert-carol" || reg.Role != "custodian" {
		t.Fatalf("TenantBarrierOfficerRegister = %+v", reg)
	}
	officers, err := tr.TenantBarrierOfficerList(ctx, "acme")
	if err != nil || len(officers) != 1 || officers[0].Username != "carol" {
		t.Fatalf("TenantBarrierOfficerList = %+v, %v", officers, err)
	}
	ch, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme", Op: "unseal", Username: "carol", Factor: "mtls"})
	if err != nil || ch.ChallengeID != "c1" || ch.Username != "carol" {
		t.Fatalf("TenantBarrierQuorumBegin = %+v, %v", ch, err)
	}
	out, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: "acme", Op: "unseal", Username: "carol", ChallengeID: "c1", Share: []byte("s")})
	if err != nil || out.Required != 2 || out.Submitted != 1 {
		t.Fatalf("TenantBarrierQuorumSubmit = %+v, %v", out, err)
	}
	round, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 2, Policy: "any-enrolled"})
	if err != nil || round.NewThreshold != 2 {
		t.Fatalf("TenantBarrierRekeyStart = %+v, %v", round, err)
	}
	rev, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "erin"})
	if err != nil || len(rev.Remove) != 1 || rev.Remove[0] != "erin" {
		t.Fatalf("TenantBarrierOfficerRevoke = %+v, %v", rev, err)
	}
	if len(svc.tenants) != 6 {
		t.Fatalf("tenant verbs reached = %q, want 6 for acme", svc.tenants)
	}
	for _, id := range svc.tenants {
		if id != "acme" {
			t.Fatalf("tenant verbs reached = %q, want acme only", svc.tenants)
		}
	}

	_, err = tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
	requireRequestError(t, err, "TenantBarrierShareClaimBegin")
	_, err = tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"})
	requireRequestError(t, err, "TenantBarrierShareClaimComplete")
}

func TestMCP_DeletedTenants_RoundTrip(t *testing.T) {
	svc := &holderTenant{mockTenantSvc: newMockTenantSvc()}
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()
	ctx := context.Background()

	page, err := tr.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: 5, PageToken: "tok"})
	if err != nil {
		t.Fatalf("ListDeletedTenants: %v", err)
	}
	if len(page.Tenants) != 1 || page.Tenants[0].ID != "gone" || page.NextPageToken != "next" {
		t.Fatalf("ListDeletedTenants = %+v", page)
	}
	if svc.pageToken != "tok" || svc.pageSize != 5 {
		t.Fatalf("service saw page %q size %d, want tok 5", svc.pageToken, svc.pageSize)
	}
	if _, err := tr.ListDeletedTenants(ctx, nil); err != nil {
		t.Fatalf("ListDeletedTenants(nil): %v", err)
	}
	if svc.pageToken != "" || svc.pageSize != 0 {
		t.Fatalf("a nil request sent page %q size %d, want the server defaults", svc.pageToken, svc.pageSize)
	}
	report, err := tr.ReconcileDeletedTenants(ctx)
	if err != nil || report.Inspected != 4 {
		t.Fatalf("ReconcileDeletedTenants = %+v, %v", report, err)
	}
}

func TestMCP_TenantHolders_RefusalsAndClosed(t *testing.T) {
	tr, cleanup := connectTenantInProcess(t, &holderTenant{mockTenantSvc: newMockTenantSvc(), err: tenant.ErrTenantNotFound})
	defer cleanup()
	ctx := context.Background()

	calls := map[string]func() error{
		"TenantBarrierOfficerRegister": func() error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme", Username: "u", CSRPEM: "c"})
			return err
		},
		"TenantBarrierOfficerList": func() error { _, err := tr.TenantBarrierOfficerList(ctx, "acme"); return err },
		"TenantBarrierQuorumBegin": func() error {
			_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme", Op: "unseal", Username: "u"})
			return err
		},
		"TenantBarrierQuorumSubmit": func() error {
			_, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: "acme", Op: "unseal", Username: "u", Share: []byte("s")})
			return err
		},
		"TenantBarrierRekeyStart": func() error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 2})
			return err
		},
		"TenantBarrierOfficerRevoke": func() error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "u"})
			return err
		},
		"ListDeletedTenants": func() error {
			_, err := tr.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{})
			return err
		},
		"ReconcileDeletedTenants": func() error { _, err := tr.ReconcileDeletedTenants(ctx); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s succeeded when the service refused", name)
		}
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, call := range calls {
		requireClosed(t, call())
	}
	_, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "u"})
	requireClosed(t, err)
	_, err = tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "u"})
	requireClosed(t, err)
}
