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

// Package enrollconv converts the protobuf messages of tenant onboarding,
// certificate enrolment and trust anchors into the transport-neutral types.
// The gRPC, Unix and QUIC transports receive the same messages, so each
// conversion lives here once. It is internal: only the transport
// implementations may import it.
package enrollconv

import (
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// parseTime reads an RFC 3339 time; empty or malformed is the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// EnrolledCert converts an EnrolledCertResponse.
func EnrolledCert(r *pb.EnrolledCertResponse) *transport.EnrolledCert {
	return &transport.EnrolledCert{
		UserID:     r.GetUserId(),
		Username:   r.GetUsername(),
		TenantID:   r.GetTenantId(),
		CertPEM:    r.GetCertPem(),
		Serial:     r.GetSerial(),
		Thumbprint: r.GetThumbprint(),
		NotAfter:   parseTime(r.GetNotAfter()),
	}
}

// Invitation converts an InvitationResponse.
func Invitation(r *pb.InvitationResponse) *transport.Invitation {
	return &transport.Invitation{
		ID:                r.GetId(),
		Token:             r.GetToken(),
		ExpiresAt:         parseTime(r.GetExpiresAt()),
		UserID:            r.GetUserId(),
		Username:          r.GetUsername(),
		TenantID:          r.GetTenantId(),
		Created:           r.GetCreated(),
		KeyPinFingerprint: r.GetKeyPinFingerprint(),
	}
}

// AdminCredential converts a TenantAdminCredential; nil is the zero value.
func AdminCredential(c *pb.TenantAdminCredential) transport.TenantAdminCredential {
	return transport.TenantAdminCredential{
		UserID:            c.GetUserId(),
		Username:          c.GetUsername(),
		CertPEM:           c.GetCertPem(),
		Serial:            c.GetSerial(),
		Thumbprint:        c.GetThumbprint(),
		AwaitsBarrierInit: c.GetAwaitsBarrierInit(),
	}
}

// TrustAnchors converts a TrustAnchorListResponse. It is never nil.
func TrustAnchors(r *pb.TrustAnchorListResponse) *transport.TrustAnchorList {
	out := &transport.TrustAnchorList{Anchors: make([]transport.TrustAnchor, 0, len(r.GetAnchors()))}
	for _, a := range r.GetAnchors() {
		out.Anchors = append(out.Anchors, transport.TrustAnchor{
			Fingerprint: a.GetFingerprint(),
			Subject:     a.GetSubject(),
			NotBefore:   parseTime(a.GetNotBefore()),
			NotAfter:    parseTime(a.GetNotAfter()),
			CertPEM:     a.GetCertPem(),
			AddedAt:     parseTime(a.GetAddedAt()),
		})
	}
	return out
}

// TenantCreateRequest builds the TenantCreateRequest message for req.
func TenantCreateRequest(req *transport.CreateTenantRequest) *pb.TenantCreateRequest {
	return &pb.TenantCreateRequest{
		Id:                req.ID,
		Name:              req.Name,
		IsolationType:     string(req.IsolationType),
		BarrierAutoUnseal: req.BarrierAutoUnseal,
		VolumePool:        req.VolumePool,
		AdminUsername:     req.AdminUsername,
		AdminCsrPem:       req.AdminCSRPEM,
		AdminCertPem:      req.AdminCertPEM,
		AdminChainPem:     req.AdminChainPEM,
		TrustAnchorsPem:   req.TrustAnchorsPEM,
	}
}
