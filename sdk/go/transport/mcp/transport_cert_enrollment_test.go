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

// IssueCert/EnrollCert/trust-anchor field-level coverage lives in
// transport_auth_test.go. This file is the remaining task-2 surface:
// CreateTenant's admin-onboarding fields over the in-process MCP transport.

package mcp

import (
	"context"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// recordingOnboardingServicer is a service.TenantOnboardingServicer that
// records the request it received and answers with a fixed admin
// credential, so a test can assert every admin field crossed the wire in
// both directions.
type recordingOnboardingServicer struct {
	lastReq *service.OnboardTenantRequest
	err     error
}

func (f *recordingOnboardingServicer) OnboardTenant(_ context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &service.OnboardTenantResult{
		Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name, IsolationType: req.IsolationType},
		Admin: service.TenantAdminCredential{
			UserID:     7,
			Username:   req.AdminUsername,
			CertPEM:    []byte("admin-cert-pem"),
			Serial:     "admin-serial",
			Thumbprint: "admin-thumb",
		},
	}, nil
}

func TestMCPCreateTenant_AdminFieldsRoundTrip(t *testing.T) {
	svc := &recordingOnboardingServicer{}
	tr, cleanup := connectTenantInProcess(t, newMockTenantSvc(), mcpapi.WithTenantOnboarding(svc))
	defer cleanup()

	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID:              "acme",
		Name:            "Acme Corp",
		IsolationType:   transport.TenantIsolationBarrier,
		AdminUsername:   "admin",
		AdminCSRPEM:     "admin-csr-pem",
		AdminCertPEM:    "admin-cert-pem-in",
		AdminChainPEM:   "admin-chain-pem",
		TrustAnchorsPEM: "anchors-pem",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if svc.lastReq == nil {
		t.Fatal("server received no request")
	}
	if svc.lastReq.AdminUsername != "admin" || string(svc.lastReq.AdminCSRPEM) != "admin-csr-pem" ||
		string(svc.lastReq.AdminCertPEM) != "admin-cert-pem-in" || string(svc.lastReq.AdminChainPEM) != "admin-chain-pem" ||
		string(svc.lastReq.TrustAnchorsPEM) != "anchors-pem" {
		t.Errorf("server received %+v", svc.lastReq)
	}
	if resp.Admin.UserID != 7 || resp.Admin.Username != "admin" || resp.Admin.CertPEM != "admin-cert-pem" ||
		resp.Admin.Serial != "admin-serial" || resp.Admin.Thumbprint != "admin-thumb" {
		t.Errorf("Admin = %+v", resp.Admin)
	}
	if resp.Admin.AwaitsBarrierInit {
		t.Error("AwaitsBarrierInit = true, want false")
	}
}

func TestMCPCreateTenant_OnboardingServiceError(t *testing.T) {
	svc := &recordingOnboardingServicer{err: testAdminUsernameRequiredError{}}
	tr, cleanup := connectTenantInProcess(t, newMockTenantSvc(), mcpapi.WithTenantOnboarding(svc))
	defer cleanup()

	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "acme", Name: "Acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

type testAdminUsernameRequiredError struct{}

func (testAdminUsernameRequiredError) Error() string { return "admin_username is required" }
