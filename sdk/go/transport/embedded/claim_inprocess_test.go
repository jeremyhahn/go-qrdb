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

package embedded

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// claimCall records what a claim handed the service layer.
type claimCall struct {
	inProcess  bool
	thumbprint string
}

// recordingClaimBarrier records the claim calls the transport makes.
type recordingClaimBarrier struct {
	service.BarrierServicer
	calls []claimCall
}

func (r *recordingClaimBarrier) BarrierShareClaimBegin(ctx context.Context, _ seal.QuorumDomain, _, thumb string) (*service.BarrierShareClaimChallenge, error) {
	r.calls = append(r.calls, claimCall{ceremony.InProcessClaim(ctx), thumb})
	return &service.BarrierShareClaimChallenge{}, nil
}

func (r *recordingClaimBarrier) BarrierShareClaimComplete(ctx context.Context, _ seal.QuorumDomain, _, _ string, _ []byte, thumb string) (*service.BarrierShareClaimResult, error) {
	r.calls = append(r.calls, claimCall{ceremony.InProcessClaim(ctx), thumb})
	return &service.BarrierShareClaimResult{}, nil
}

// recordingClaimTenant records the tenant claim calls the transport makes.
type recordingClaimTenant struct {
	mockTenantService
	calls []claimCall
}

func (r *recordingClaimTenant) TenantBarrierShareClaimBegin(ctx context.Context, _, _, thumb string) (*ceremony.ChallengeResponse, error) {
	r.calls = append(r.calls, claimCall{ceremony.InProcessClaim(ctx), thumb})
	return &ceremony.ChallengeResponse{}, nil
}

func (r *recordingClaimTenant) TenantBarrierShareClaimComplete(ctx context.Context, _, _, _ string, _ []byte, thumb string) (*ceremony.ShareClaimResult, error) {
	r.calls = append(r.calls, claimCall{ceremony.InProcessClaim(ctx), thumb})
	return &ceremony.ShareClaimResult{}, nil
}

// claimCalls drives all six claim steps through tr and tenantTr with ctx.
func claimCalls(t *testing.T, ctx context.Context, tr, tenantTr *Transport) {
	t.Helper()
	begin := &transport.ShareClaimBeginRequest{Username: "carol"}
	complete := &transport.ShareClaimCompleteRequest{Username: "carol"}
	steps := []func() error{
		func() error { _, err := tr.ShareClaimBegin(ctx, begin); return err },
		func() error { _, err := tr.ShareClaimComplete(ctx, complete); return err },
		func() error { _, err := tr.RecoveryShareClaimBegin(ctx, begin); return err },
		func() error { _, err := tr.RecoveryShareClaimComplete(ctx, complete); return err },
		func() error {
			_, err := tenantTr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
			return err
		},
		func() error {
			_, err := tenantTr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"})
			return err
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("claim step %d: %v", i, err)
		}
	}
}

// newClaimTransports builds a barrier and a tenant transport over recording
// services.
func newClaimTransports(t *testing.T) (*Transport, *recordingClaimBarrier, *Transport, *recordingClaimTenant) {
	t.Helper()
	barrier := &recordingClaimBarrier{}
	tenantSvc := &recordingClaimTenant{}
	tenantTr, err := New(newMockKVService(), &mockAdminService{}, WithTenantService(tenantSvc))
	if err != nil {
		t.Fatal(err)
	}
	return newBarrierTransport(t, barrier), barrier, tenantTr, tenantSvc
}

// TestEmbeddedClaim_WithoutACertificateIsAnInProcessClaim proves an embedded
// claim with no TLS peer certificate on ctx reaches the service as an
// in-process claim: marked with ceremony.WithInProcessClaim and presenting no
// thumbprint, so the ceremony binds it to the enrolled certificate and
// releases the envelope only for the nonce signature by its key.
func TestEmbeddedClaim_WithoutACertificateIsAnInProcessClaim(t *testing.T) {
	tr, barrier, tenantTr, tenantSvc := newClaimTransports(t)
	claimCalls(t, context.Background(), tr, tenantTr)
	for _, c := range append(barrier.calls, tenantSvc.calls...) {
		if !c.inProcess || c.thumbprint != "" {
			t.Fatalf("call = %+v, want an in-process claim presenting no thumbprint", c)
		}
	}
	if len(barrier.calls) != 4 || len(tenantSvc.calls) != 2 {
		t.Fatalf("calls = %d barrier, %d tenant; want 4 and 2", len(barrier.calls), len(tenantSvc.calls))
	}
}

// TestEmbeddedClaim_APresentedCertificateIsChecked proves a claim whose ctx
// carries a verified TLS peer certificate presents that certificate and is
// not marked in process, exactly as on a network transport.
func TestEmbeddedClaim_APresentedCertificateIsChecked(t *testing.T) {
	ctx := ctxWithEmbeddedPeerCert(t)
	want := mtls.Thumbprint(auth.TLSStateFromContext(ctx).PeerCertificates[0].Raw)
	tr, barrier, tenantTr, tenantSvc := newClaimTransports(t)
	claimCalls(t, ctx, tr, tenantTr)
	for _, c := range append(barrier.calls, tenantSvc.calls...) {
		if c.inProcess || c.thumbprint != want {
			t.Fatalf("call = %+v, want the presented thumbprint %s and no in-process mark", c, want)
		}
	}
}
