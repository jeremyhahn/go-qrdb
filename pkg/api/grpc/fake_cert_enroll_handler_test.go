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

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// fakeCertEnrollHandler is a configurable, faithful
// service.CertEnrollmentServicer double for the IssueCert/EnrollCert/
// TrustAnchor* and delegation-policy handler tests. It carries no build
// constraint because its callers span every tag set.
type fakeCertEnrollHandler struct {
	signFn   func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollFn func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addFn    func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listFn   func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeFn func(ctx context.Context, tenantID, fingerprint string) error
	delegFn  func(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error)

	policyPutFn    func(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error)
	policyListFn   func(ctx context.Context, tenantID string) ([]delegation.Policy, error)
	policyDeleteFn func(ctx context.Context, tenantID, principal string) error
}

var _ service.CertEnrollmentServicer = (*fakeCertEnrollHandler)(nil)

func (f *fakeCertEnrollHandler) IssueDelegation(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	return f.delegFn(ctx, req)
}

func (f *fakeCertEnrollHandler) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.signFn(ctx, req)
}

func (f *fakeCertEnrollHandler) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.enrollFn(ctx, req)
}

func (f *fakeCertEnrollHandler) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	return f.addFn(ctx, tenantID, bundlePEM)
}

func (f *fakeCertEnrollHandler) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	return f.listFn(ctx, tenantID)
}

func (f *fakeCertEnrollHandler) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	return f.removeFn(ctx, tenantID, fingerprint)
}
