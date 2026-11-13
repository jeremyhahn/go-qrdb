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

//go:build !integration

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// fakeCertEnrollHandler is a configurable, faithful
// service.CertEnrollmentServicer double for the IssueCert/EnrollCert/
// TrustAnchor* handler tests below.
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

// ---------------------------------------------------------------------------
// IssueCert
// ---------------------------------------------------------------------------

func TestGRPC_IssueCert_Success(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeCertEnrollHandler{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.Username != "alice" {
				t.Fatalf("expected username 'alice', got %q", req.Username)
			}
			return &service.EnrolledCertificate{
				UserID:     7,
				Username:   "alice",
				TenantID:   req.TenantID,
				CertPEM:    []byte("-----BEGIN CERTIFICATE-----\nfake-cert\n-----END CERTIFICATE-----"),
				Serial:     "ABCDEF01",
				Thumbprint: "thumb-1",
				NotAfter:   notAfter,
			}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.IssueCert(context.Background(), &pb.IssueCertRequest{
		Username: "alice", CsrPem: "csr-pem",
	})
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}
	if resp.GetUserId() != 7 {
		t.Errorf("expected userID 7, got %d", resp.GetUserId())
	}
	if resp.GetUsername() != "alice" {
		t.Errorf("expected username 'alice', got %q", resp.GetUsername())
	}
	if resp.GetSerial() != "ABCDEF01" {
		t.Errorf("expected serial 'ABCDEF01', got %q", resp.GetSerial())
	}
	if resp.GetThumbprint() != "thumb-1" {
		t.Errorf("expected thumbprint 'thumb-1', got %q", resp.GetThumbprint())
	}
	if resp.GetCertPem() == "" {
		t.Error("expected non-empty certPem")
	}
	if resp.GetNotAfter() == "" {
		t.Error("expected non-empty notAfter")
	}
}

func TestGRPC_IssueCert_InvalidTTL(t *testing.T) {
	fake := &fakeCertEnrollHandler{}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.IssueCert(context.Background(), &pb.IssueCertRequest{
		Username: "alice", CsrPem: "csr-pem", Ttl: "bad-ttl",
	})
	if err == nil {
		t.Fatal("expected error for invalid TTL, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_IssueCert_ServiceError(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &service.CertEnrollmentFieldError{Field: "csr_pem"}
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.IssueCert(context.Background(), &pb.IssueCertRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_IssueCert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IssueCert(context.Background(), &pb.IssueCertRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// EnrollCert
// ---------------------------------------------------------------------------

func TestGRPC_EnrollCert_Success(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		enrollFn: func(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.Username != "dave" {
				t.Fatalf("expected username 'dave', got %q", req.Username)
			}
			return &service.EnrolledCertificate{Username: "dave", Thumbprint: "thumb-2"}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.EnrollCert(context.Background(), &pb.EnrollCertRequest{
		Username: "dave", CertPem: "leaf-pem",
	})
	if err != nil {
		t.Fatalf("EnrollCert failed: %v", err)
	}
	if resp.GetThumbprint() != "thumb-2" {
		t.Errorf("expected thumbprint 'thumb-2', got %q", resp.GetThumbprint())
	}
	if resp.GetSerial() != "" {
		t.Errorf("expected empty serial for a BYOC enrolment, got %q", resp.GetSerial())
	}
}

func TestGRPC_EnrollCert_ServiceError(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		enrollFn: func(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, &trustanchor.NoAnchorsError{TenantID: "acme"}
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.EnrollCert(context.Background(), &pb.EnrollCertRequest{Username: "dave", CertPem: "leaf-pem"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestGRPC_EnrollCert_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.EnrollCert(context.Background(), &pb.EnrollCertRequest{Username: "dave"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// TrustAnchorAdd / TrustAnchorList / TrustAnchorRemove
// ---------------------------------------------------------------------------

func TestGRPC_TrustAnchorAdd_Success(t *testing.T) {
	addedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeCertEnrollHandler{
		addFn: func(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			if string(bundlePEM) != "ca-pem" {
				t.Fatalf("expected bundlePEM 'ca-pem', got %q", string(bundlePEM))
			}
			return []trustanchor.Anchor{{
				Fingerprint: "fp-1", Subject: "CN=acme-root", CertPEM: []byte("ca-pem"), AddedAt: addedAt,
			}}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.TrustAnchorAdd(context.Background(), &pb.TrustAnchorAddRequest{
		TenantId: "acme", CertsPem: "ca-pem",
	})
	if err != nil {
		t.Fatalf("TrustAnchorAdd failed: %v", err)
	}
	if len(resp.GetAnchors()) != 1 {
		t.Fatalf("expected 1 anchor, got %d", len(resp.GetAnchors()))
	}
	if resp.GetAnchors()[0].GetFingerprint() != "fp-1" {
		t.Errorf("expected fingerprint 'fp-1', got %q", resp.GetAnchors()[0].GetFingerprint())
	}
}

func TestGRPC_TrustAnchorAdd_ServiceError(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		addFn: func(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
			return nil, &trustanchor.EmptyBundleError{}
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.TrustAnchorAdd(context.Background(), &pb.TrustAnchorAddRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_TrustAnchorAdd_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TrustAnchorAdd(context.Background(), &pb.TrustAnchorAddRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TrustAnchorList_Success(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		listFn: func(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			return []trustanchor.Anchor{{Fingerprint: "fp-1"}, {Fingerprint: "fp-2"}}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.TrustAnchorList(context.Background(), &pb.TrustAnchorListRequest{TenantId: "acme"})
	if err != nil {
		t.Fatalf("TrustAnchorList failed: %v", err)
	}
	if len(resp.GetAnchors()) != 2 {
		t.Fatalf("expected 2 anchors, got %d", len(resp.GetAnchors()))
	}
}

func TestGRPC_TrustAnchorList_ServiceError(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		listFn: func(context.Context, string) ([]trustanchor.Anchor, error) {
			return nil, errors.New("store unavailable")
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.TrustAnchorList(context.Background(), &pb.TrustAnchorListRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TrustAnchorList_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TrustAnchorList(context.Background(), &pb.TrustAnchorListRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TrustAnchorRemove_Success(t *testing.T) {
	var gotTenant, gotFingerprint string
	fake := &fakeCertEnrollHandler{
		removeFn: func(_ context.Context, tenantID, fingerprint string) error {
			gotTenant, gotFingerprint = tenantID, fingerprint
			return nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.TrustAnchorRemove(context.Background(), &pb.TrustAnchorRemoveRequest{
		TenantId: "acme", Fingerprint: "fp-1",
	})
	if err != nil {
		t.Fatalf("TrustAnchorRemove failed: %v", err)
	}
	if gotTenant != "acme" || gotFingerprint != "fp-1" {
		t.Errorf("RemoveTrustAnchor(%q, %q), want (acme, fp-1)", gotTenant, gotFingerprint)
	}
}

func TestGRPC_TrustAnchorRemove_ServiceError(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		removeFn: func(context.Context, string, string) error {
			return &trustanchor.AnchorNotFoundError{Fingerprint: "fp-missing"}
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	_, err := client.TrustAnchorRemove(context.Background(), &pb.TrustAnchorRemoveRequest{
		TenantId: "acme", Fingerprint: "fp-missing",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_TrustAnchorRemove_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TrustAnchorRemove(context.Background(), &pb.TrustAnchorRemoveRequest{TenantId: "acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}
