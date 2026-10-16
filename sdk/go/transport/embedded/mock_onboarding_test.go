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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// mockCertEnrollmentServicer implements service.CertEnrollmentServicer with
// function-field injection, shared by every embedded test that exercises
// IssueCert, EnrollCert and the trust-anchor verbs.
type mockCertEnrollmentServicer struct {
	signCertificateFn   func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollCertificateFn func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addTrustAnchorsFn   func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listTrustAnchorsFn  func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeTrustAnchorFn func(ctx context.Context, tenantID, fingerprint string) error
	issueDelegationFn   func(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error)
}

var _ service.CertEnrollmentServicer = (*mockCertEnrollmentServicer)(nil)

func (m *mockCertEnrollmentServicer) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	return m.signCertificateFn(ctx, req)
}

func (m *mockCertEnrollmentServicer) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	return m.enrollCertificateFn(ctx, req)
}

func (m *mockCertEnrollmentServicer) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	return m.addTrustAnchorsFn(ctx, tenantID, bundlePEM)
}

func (m *mockCertEnrollmentServicer) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	return m.listTrustAnchorsFn(ctx, tenantID)
}

func (m *mockCertEnrollmentServicer) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	return m.removeTrustAnchorFn(ctx, tenantID, fingerprint)
}

func (m *mockCertEnrollmentServicer) IssueDelegation(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	return m.issueDelegationFn(ctx, req)
}

// mockTenantOnboardingServicer implements service.TenantOnboardingServicer
// with function-field injection, shared by every embedded test that
// exercises CreateTenant's admin-onboarding fields.
type mockTenantOnboardingServicer struct {
	onboardTenantFn func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error)
}

var _ service.TenantOnboardingServicer = (*mockTenantOnboardingServicer)(nil)

func (m *mockTenantOnboardingServicer) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	return m.onboardTenantFn(ctx, req)
}
