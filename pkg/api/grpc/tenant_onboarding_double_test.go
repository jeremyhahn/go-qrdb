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

package grpc

// This double has no build constraint: it is shared by the full-feature
// tenant tests (service_tenant_test.go) and the minimal-tag ones
// (grpc_coverage5_test.go), so its constraint is the union of theirs.

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeTenantOnboarding is a faithful service.TenantOnboardingServicer double:
// it echoes exactly what OnboardTenant produces in production (a tenant and
// its first admin's credential), with no onboarding logic of its own to drift
// from the real bootstrap.ProvisionTenant path these gRPC tests do not drive.
type fakeTenantOnboarding struct {
	result *service.OnboardTenantResult
	err    error
	gotReq *service.OnboardTenantRequest
}

var _ service.TenantOnboardingServicer = (*fakeTenantOnboarding)(nil)

func (f *fakeTenantOnboarding) OnboardTenant(_ context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	f.gotReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &service.OnboardTenantResult{
		Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name},
		Admin:  service.TenantAdminCredential{Username: req.AdminUsername},
	}, nil
}
