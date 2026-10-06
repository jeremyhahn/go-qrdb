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
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// ---------------------------------------------------------------------------
// handleTrustAnchorAdd
// ---------------------------------------------------------------------------

func TestHandleTrustAnchorAdd_Success(t *testing.T) {
	addedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notBefore := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeCertEnroll{
		addFn: func(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			if string(bundlePEM) != "ca-pem" {
				t.Fatalf("expected bundlePEM 'ca-pem', got %q", string(bundlePEM))
			}
			return []trustanchor.Anchor{{
				Fingerprint: "fp-1",
				Subject:     "CN=acme-root",
				NotBefore:   notBefore,
				NotAfter:    notAfter,
				CertPEM:     []byte("ca-pem"),
				AddedAt:     addedAt,
			}}, nil
		},
	}

	srv := newCertEnrollServer(fake)
	payload, err := proto.Marshal(&pb.TrustAnchorAddRequest{TenantId: "acme", CertsPem: "ca-pem"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := srv.handleTrustAnchorAdd(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out pb.TrustAnchorListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.GetAnchors()) != 1 {
		t.Fatalf("expected 1 anchor, got %d", len(out.GetAnchors()))
	}
	got := out.GetAnchors()[0]
	if got.GetFingerprint() != "fp-1" {
		t.Errorf("expected fingerprint 'fp-1', got %q", got.GetFingerprint())
	}
	if got.GetSubject() != "CN=acme-root" {
		t.Errorf("expected subject 'CN=acme-root', got %q", got.GetSubject())
	}
	if got.GetAddedAt() == "" {
		t.Error("expected non-empty addedAt")
	}
}

func TestHandleTrustAnchorAdd_ServiceError(t *testing.T) {
	fake := &fakeCertEnroll{
		addFn: func(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
			return nil, &trustanchor.EmptyBundleError{}
		},
	}
	srv := newCertEnrollServer(fake)
	payload, _ := proto.Marshal(&pb.TrustAnchorAddRequest{TenantId: "acme", CertsPem: ""})

	_, err := srv.handleTrustAnchorAdd(context.Background(), payload)
	var ebe *trustanchor.EmptyBundleError
	if !errors.As(err, &ebe) {
		t.Fatalf("expected *trustanchor.EmptyBundleError, got %T: %v", err, err)
	}
}

func TestHandleTrustAnchorAdd_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.TrustAnchorAddRequest{TenantId: "acme", CertsPem: "ca-pem"})

	_, err := srv.handleTrustAnchorAdd(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleTrustAnchorAdd_InvalidWire(t *testing.T) {
	srv := newCertEnrollServer(&fakeCertEnroll{})

	_, err := srv.handleTrustAnchorAdd(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTrustAnchorList
// ---------------------------------------------------------------------------

func TestHandleTrustAnchorList_Success(t *testing.T) {
	fake := &fakeCertEnroll{
		listFn: func(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			return []trustanchor.Anchor{
				{Fingerprint: "fp-1", Subject: "CN=a"},
				{Fingerprint: "fp-2", Subject: "CN=b"},
			}, nil
		},
	}
	srv := newCertEnrollServer(fake)
	payload, _ := proto.Marshal(&pb.TrustAnchorListRequest{TenantId: "acme"})

	resp, err := srv.handleTrustAnchorList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.TrustAnchorListResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.GetAnchors()) != 2 {
		t.Fatalf("expected 2 anchors, got %d", len(out.GetAnchors()))
	}
}

func TestHandleTrustAnchorList_ServiceError(t *testing.T) {
	fake := &fakeCertEnroll{
		listFn: func(context.Context, string) ([]trustanchor.Anchor, error) {
			return nil, errMockCert
		},
	}
	srv := newCertEnrollServer(fake)
	payload, _ := proto.Marshal(&pb.TrustAnchorListRequest{TenantId: "acme"})

	_, err := srv.handleTrustAnchorList(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleTrustAnchorList_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.TrustAnchorListRequest{TenantId: "acme"})

	_, err := srv.handleTrustAnchorList(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleTrustAnchorRemove
// ---------------------------------------------------------------------------

func TestHandleTrustAnchorRemove_Success(t *testing.T) {
	var gotTenant, gotFingerprint string
	fake := &fakeCertEnroll{
		removeFn: func(_ context.Context, tenantID, fingerprint string) error {
			gotTenant, gotFingerprint = tenantID, fingerprint
			return nil
		},
	}
	srv := newCertEnrollServer(fake)
	payload, _ := proto.Marshal(&pb.TrustAnchorRemoveRequest{TenantId: "acme", Fingerprint: "fp-1"})

	resp, err := srv.handleTrustAnchorRemove(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
	if gotTenant != "acme" || gotFingerprint != "fp-1" {
		t.Errorf("RemoveTrustAnchor(%q, %q), want (acme, fp-1)", gotTenant, gotFingerprint)
	}
}

func TestHandleTrustAnchorRemove_ServiceError(t *testing.T) {
	fake := &fakeCertEnroll{
		removeFn: func(context.Context, string, string) error {
			return &trustanchor.AnchorNotFoundError{Fingerprint: "fp-missing"}
		},
	}
	srv := newCertEnrollServer(fake)
	payload, _ := proto.Marshal(&pb.TrustAnchorRemoveRequest{TenantId: "acme", Fingerprint: "fp-missing"})

	_, err := srv.handleTrustAnchorRemove(context.Background(), payload)
	var nf *trustanchor.AnchorNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *trustanchor.AnchorNotFoundError, got %T: %v", err, err)
	}
}

func TestHandleTrustAnchorRemove_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.TrustAnchorRemoveRequest{TenantId: "acme", Fingerprint: "fp-1"})

	_, err := srv.handleTrustAnchorRemove(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}
