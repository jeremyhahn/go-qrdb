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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// marshalAnchors encodes a tenant's trust anchors as a TrustAnchorListResponse.
func marshalAnchors(anchors []trustanchor.Anchor) ([]byte, error) {
	out := &pb.TrustAnchorListResponse{Anchors: make([]*pb.TrustAnchor, 0, len(anchors))}
	for _, a := range anchors {
		ta := &pb.TrustAnchor{
			Fingerprint: a.Fingerprint,
			Subject:     a.Subject,
			NotBefore:   a.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:    a.NotAfter.UTC().Format(time.RFC3339),
			CertPem:     string(a.CertPEM),
		}
		if !a.AddedAt.IsZero() {
			ta.AddedAt = a.AddedAt.UTC().Format(time.RFC3339)
		}
		out.Anchors = append(out.Anchors, ta)
	}
	resp, err := proto.Marshal(out)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleTrustAnchorAdd registers trust anchors of the request's tenant, which
// must be the caller's own.
func (s *Server) handleTrustAnchorAdd(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certEnroll == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert enrollment"}
	}
	var req pb.TrustAnchorAddRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.TrustAnchorsAdd); err != nil {
		return nil, err
	}
	anchors, err := s.certEnroll.AddTrustAnchors(ctx, req.GetTenantId(), []byte(req.GetCertsPem()))
	if err != nil {
		return nil, err
	}
	return marshalAnchors(anchors)
}

// handleTrustAnchorList lists the request's tenant's trust anchors (the
// caller's own).
func (s *Server) handleTrustAnchorList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certEnroll == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert enrollment"}
	}
	var req pb.TrustAnchorListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.TrustAnchorsList); err != nil {
		return nil, err
	}
	anchors, err := s.certEnroll.ListTrustAnchors(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return marshalAnchors(anchors)
}

// handleTrustAnchorRemove withdraws one of the request's tenant's trust
// anchors (the caller's own).
func (s *Server) handleTrustAnchorRemove(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certEnroll == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert enrollment"}
	}
	var req pb.TrustAnchorRemoveRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.TrustAnchorsRemove); err != nil {
		return nil, err
	}
	if err := s.certEnroll.RemoveTrustAnchor(ctx, req.GetTenantId(), req.GetFingerprint()); err != nil {
		return nil, err
	}
	return nil, nil
}
