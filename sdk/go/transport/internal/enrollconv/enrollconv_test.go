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

package enrollconv

import (
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEnrolledCert(t *testing.T) {
	notAfter := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	in := &pb.EnrolledCertResponse{
		UserId:     7,
		Username:   "alice",
		TenantId:   "acme",
		CertPem:    "cert-pem",
		Serial:     "abc123",
		Thumbprint: "thumb-1",
		NotAfter:   notAfter.Format(time.RFC3339),
	}
	got := EnrolledCert(in)
	want := &transport.EnrolledCert{
		UserID:     7,
		Username:   "alice",
		TenantID:   "acme",
		CertPEM:    "cert-pem",
		Serial:     "abc123",
		Thumbprint: "thumb-1",
		NotAfter:   notAfter,
	}
	if *got != *want {
		t.Errorf("EnrolledCert() = %+v, want %+v", got, want)
	}
}

func TestEnrolledCert_EmptyNotAfter(t *testing.T) {
	got := EnrolledCert(&pb.EnrolledCertResponse{Username: "alice"})
	if !got.NotAfter.IsZero() {
		t.Errorf("NotAfter = %v, want zero", got.NotAfter)
	}
}

func TestEnrolledCert_BringYourOwn_SerialEmpty(t *testing.T) {
	// A bring-your-own certificate carries no serial: the cluster did not
	// issue it. The conversion must not synthesize one.
	got := EnrolledCert(&pb.EnrolledCertResponse{Username: "alice", Thumbprint: "thumb-byoc"})
	if got.Serial != "" {
		t.Errorf("Serial = %q, want empty", got.Serial)
	}
	if got.Thumbprint != "thumb-byoc" {
		t.Errorf("Thumbprint = %q, want thumb-byoc", got.Thumbprint)
	}
}

func TestAdminCredential(t *testing.T) {
	in := &pb.TenantAdminCredential{
		UserId:            42,
		Username:          "admin",
		CertPem:           "admin-cert-pem",
		Serial:            "ser-1",
		Thumbprint:        "thumb-admin",
		AwaitsBarrierInit: true,
	}
	got := AdminCredential(in)
	want := transport.TenantAdminCredential{
		UserID:            42,
		Username:          "admin",
		CertPEM:           "admin-cert-pem",
		Serial:            "ser-1",
		Thumbprint:        "thumb-admin",
		AwaitsBarrierInit: true,
	}
	if got != want {
		t.Errorf("AdminCredential() = %+v, want %+v", got, want)
	}
}

// TestAdminCredential_Nil: a nil *pb.TenantAdminCredential converts to the
// zero value rather than panicking -- a generated proto getter on a nil
// receiver returns the zero value for every field, which this conversion
// relies on.
func TestAdminCredential_Nil(t *testing.T) {
	got := AdminCredential(nil)
	if got != (transport.TenantAdminCredential{}) {
		t.Errorf("AdminCredential(nil) = %+v, want the zero value", got)
	}
}

func TestAdminCredential_AwaitsBarrierInit_UserIDZero(t *testing.T) {
	// UserID is zero while AwaitsBarrierInit: the admin's account is created
	// only when the admin initializes the tenant's own barrier.
	got := AdminCredential(&pb.TenantAdminCredential{
		Username:          "admin",
		AwaitsBarrierInit: true,
	})
	if got.UserID != 0 {
		t.Errorf("UserID = %d, want 0", got.UserID)
	}
	if !got.AwaitsBarrierInit {
		t.Error("AwaitsBarrierInit = false, want true")
	}
}

func TestTrustAnchors_Empty(t *testing.T) {
	got := TrustAnchors(&pb.TrustAnchorListResponse{})
	if got == nil {
		t.Fatal("TrustAnchors() returned nil, want a non-nil empty list")
	}
	if len(got.Anchors) != 0 {
		t.Errorf("Anchors = %+v, want empty", got.Anchors)
	}
}

func TestTrustAnchors_Multiple(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	addedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	in := &pb.TrustAnchorListResponse{
		Anchors: []*pb.TrustAnchor{
			{
				Fingerprint: "fp1",
				Subject:     "CN=ca1",
				NotBefore:   notBefore.Format(time.RFC3339),
				NotAfter:    notAfter.Format(time.RFC3339),
				CertPem:     "ca1-pem",
				AddedAt:     addedAt.Format(time.RFC3339),
			},
			{
				Fingerprint: "fp2",
				Subject:     "CN=ca2",
				// No AddedAt: an anchor store that does not record it.
			},
		},
	}
	got := TrustAnchors(in)
	if len(got.Anchors) != 2 {
		t.Fatalf("got %d anchors, want 2", len(got.Anchors))
	}
	a0 := got.Anchors[0]
	if a0.Fingerprint != "fp1" || a0.Subject != "CN=ca1" || a0.CertPEM != "ca1-pem" {
		t.Errorf("anchor[0] = %+v", a0)
	}
	if !a0.NotBefore.Equal(notBefore) || !a0.NotAfter.Equal(notAfter) || !a0.AddedAt.Equal(addedAt) {
		t.Errorf("anchor[0] times = %+v", a0)
	}
	a1 := got.Anchors[1]
	if a1.Fingerprint != "fp2" {
		t.Errorf("anchor[1].Fingerprint = %q, want fp2", a1.Fingerprint)
	}
	if !a1.AddedAt.IsZero() {
		t.Errorf("anchor[1].AddedAt = %v, want zero", a1.AddedAt)
	}
}

func TestTenantCreateRequest(t *testing.T) {
	req := &transport.CreateTenantRequest{
		ID:                "t1",
		Name:              "Tenant One",
		IsolationType:     transport.TenantIsolationBarrier,
		BarrierAutoUnseal: true,
		VolumePool:        "fast",
		AdminUsername:     "admin",
		AdminCSRPEM:       "csr-pem",
		AdminCertPEM:      "cert-pem",
		AdminChainPEM:     "chain-pem",
		TrustAnchorsPEM:   "anchors-pem",
	}
	got := TenantCreateRequest(req)
	want := &pb.TenantCreateRequest{
		Id:                "t1",
		Name:              "Tenant One",
		IsolationType:     "barrier",
		BarrierAutoUnseal: true,
		VolumePool:        "fast",
		AdminUsername:     "admin",
		AdminCsrPem:       "csr-pem",
		AdminCertPem:      "cert-pem",
		AdminChainPem:     "chain-pem",
		TrustAnchorsPem:   "anchors-pem",
	}
	if got.GetId() != want.GetId() || got.GetName() != want.GetName() ||
		got.GetIsolationType() != want.GetIsolationType() ||
		got.GetBarrierAutoUnseal() != want.GetBarrierAutoUnseal() ||
		got.GetVolumePool() != want.GetVolumePool() ||
		got.GetAdminUsername() != want.GetAdminUsername() ||
		got.GetAdminCsrPem() != want.GetAdminCsrPem() ||
		got.GetAdminCertPem() != want.GetAdminCertPem() ||
		got.GetAdminChainPem() != want.GetAdminChainPem() ||
		got.GetTrustAnchorsPem() != want.GetTrustAnchorsPem() {
		t.Errorf("TenantCreateRequest() = %+v, want %+v", got, want)
	}
}

func TestTenantCreateRequest_EmptyFields(t *testing.T) {
	got := TenantCreateRequest(&transport.CreateTenantRequest{ID: "t2", Name: "Tenant Two"})
	if got.GetId() != "t2" || got.GetName() != "Tenant Two" {
		t.Errorf("TenantCreateRequest() = %+v", got)
	}
	if got.GetAdminUsername() != "" || got.GetAdminCsrPem() != "" || got.GetAdminCertPem() != "" ||
		got.GetAdminChainPem() != "" || got.GetTrustAnchorsPem() != "" || got.GetIsolationType() != "" ||
		got.GetBarrierAutoUnseal() || got.GetVolumePool() != "" {
		t.Errorf("TenantCreateRequest() with empty fields = %+v, want every optional field empty", got)
	}
}

func TestParseTime(t *testing.T) {
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{"empty", "", time.Time{}},
		{"malformed", "not-a-time", time.Time{}},
		{"valid", ts.Format(time.RFC3339), ts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTime(tc.in); !got.Equal(tc.want) {
				t.Errorf("parseTime(%q) = %v, want %v", tc.in, got, tc.want)
			}
			// And through its public callers, so the helper is proven wired
			// in, not just correct in isolation.
			got := EnrolledCert(&pb.EnrolledCertResponse{NotAfter: tc.in})
			if !got.NotAfter.Equal(tc.want) {
				t.Errorf("parseTime(%q) via EnrolledCert = %v, want %v", tc.in, got.NotAfter, tc.want)
			}
		})
	}
}
