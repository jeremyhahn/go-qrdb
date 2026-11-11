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
	"context"
	"errors"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenant "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// onboardingFromTenant is a TenantOnboardingServicer that delegates straight
// to svc.CreateTenant, so a test exercising CreateTenant's refusals against a
// hand-built tenant.TenantServicer double does not need to assemble the
// production onboarding service's full dependency graph (users, roles,
// certs, trust anchors).
func onboardingFromTenant(svc tenant.TenantServicer) *mockCertOnboardingSvc {
	return &mockCertOnboardingSvc{
		onboardFn: func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			info, err := svc.CreateTenant(ctx, req.ID, req.Name, req.IsolationType)
			if err != nil {
				return nil, err
			}
			return &service.OnboardTenantResult{Tenant: info}, nil
		},
	}
}

// mockCertOnboardingSvc implements service.TenantOnboardingServicer.
type mockCertOnboardingSvc struct {
	onboardFn func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error)
}

func (m *mockCertOnboardingSvc) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	return m.onboardFn(ctx, req)
}

var _ service.TenantOnboardingServicer = (*mockCertOnboardingSvc)(nil)

// partialReconcileSvc answers ReconcileDeletedTenants as the real service does
// when one retired id failed and another was reclaimed: the report, whose
// Failed list copies the aggregate error returned with it (pinned in
// pkg/tenant).
type partialReconcileSvc struct {
	tenant.TenantServicer
}

func (p partialReconcileSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	boom := errors.New("device busy")
	// Bound to names, not returned as two literals: go1.27's gofmt and
	// golangci-lint's gofmt indent a multi-value return of composite
	// literals differently, so no layout of that form satisfies both.
	report := &tenant.ReconcileReport{
		Inspected: 2,
		Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
		Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
	}
	failures := &tenant.ResidueReconcileFailuresError{Errors: []*tenant.ResidueReconcileError{
		{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom},
	}}
	return report, failures
}

// assertPartial checks a client saw both halves of a partly failed pass.
func assertPartial(t *testing.T, resp *transport.ReconcileDeletedTenantsResponse, err error) *transport.ReconcileDeletedTenantsError {
	t.Helper()
	if resp == nil || resp.Inspected != 2 || len(resp.Reclaimed) != 1 || resp.Reclaimed[0].TenantID != "vb" {
		t.Fatalf("resp = %+v; want vb reclaimed out of 2", resp)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].TenantID != "va" || resp.Failed[0].Op != "remove" ||
		resp.Failed[0].Path != "/srv/va" || resp.Failed[0].Error != "device busy" {
		t.Fatalf("failed = %+v; want va's removal failure", resp.Failed)
	}
	var re *transport.ReconcileDeletedTenantsError
	if !errors.As(err, &re) || len(re.Failed) != 1 || re.Failed[0].TenantID != "va" {
		t.Fatalf("err = %v; want *transport.ReconcileDeletedTenantsError naming va", err)
	}
	return re
}

// TestUnixRT_CreateTenant_DataRootOverlapIsAFailedPrecondition covered
// CreateTenant's refusal of an explicit --data-root a retired tenant's
// unreclaimed record still claimed, answered over the socket as
// FailedPrecondition with the remedy. CreateTenant no longer accepts an
// explicit disk path (a volume tenant's reserved path is always its own
// tenant id), so two different tenant ids can never contend for the same
// literal path through this API any more, and the error type itself
// (*tenant.DataRootOverlapError) is gone; see the identical note on
// pkg/tenant's deleted TestCreateTenant_RefusesARootAStaleRecordStillClaims.

// TestUnixRT_ReconcileDeletedTenants_PartialFailureCarriesBoth: the response
// carries the report and the failures, and the client returns both.
func TestUnixRT_ReconcileDeletedTenants_PartialFailureCarriesBoth(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(partialReconcileSvc{TenantServicer: &mockTenantSvc{}}))
	defer cleanup()
	resp, err := tr.ReconcileDeletedTenants(context.Background())
	assertPartial(t, resp, err)
}
