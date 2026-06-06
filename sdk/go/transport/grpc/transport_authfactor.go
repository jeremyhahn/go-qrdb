// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	"context"
	"time"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.AuthFactorClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// AuthFactorClient implementation
// ---------------------------------------------------------------------------

// EnrollFactor enrolls a custodian in the given auth factor type via gRPC.
func (t *Transport) EnrollFactor(ctx context.Context, req *transport.EnrollFactorRequest) (*transport.EnrollFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	grpcReq := &grpcpkg.GRPCEnrollFactorRequest{
		FactorName:   req.FactorName,
		Subject:      req.Subject,
		TenantID:     req.TenantID,
		MethodParams: req.MethodParams,
	}

	resp, err := grpcpkg.EnrollFactorUnary(ctx, t.conn, grpcReq)
	if err != nil {
		return nil, mapGRPCError("EnrollFactor", err)
	}

	enrolledAt, _ := time.Parse(time.RFC3339, resp.EnrolledAt)
	return &transport.EnrollFactorResponse{
		EnrollmentID: resp.EnrollmentID,
		FactorName:   resp.FactorName,
		UserID:       resp.UserID,
		TenantID:     resp.TenantID,
		EnrolledAt:   enrolledAt,
	}, nil
}

// ChallengeFactor issues a challenge for a previously enrolled factor via gRPC.
func (t *Transport) ChallengeFactor(ctx context.Context, req *transport.ChallengeFactorRequest) (*transport.ChallengeFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	grpcReq := &grpcpkg.GRPCChallengeFactorRequest{
		EnrollmentID: req.EnrollmentID,
	}

	resp, err := grpcpkg.ChallengeFactorUnary(ctx, t.conn, grpcReq)
	if err != nil {
		return nil, mapGRPCError("ChallengeFactor", err)
	}

	expiresAt, _ := time.Parse(time.RFC3339, resp.ExpiresAt)
	return &transport.ChallengeFactorResponse{
		ChallengeID:  resp.ChallengeID,
		EnrollmentID: resp.EnrollmentID,
		Nonce:        resp.Nonce,
		ExpiresAt:    expiresAt,
		MethodData:   resp.MethodData,
	}, nil
}

// VerifyFactor verifies the custodian's challenge response via gRPC.
func (t *Transport) VerifyFactor(ctx context.Context, req *transport.VerifyFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	grpcReq := &grpcpkg.GRPCVerifyFactorRequest{
		FactorName:  req.FactorName,
		ChallengeID: req.ChallengeID,
		Value:       req.Value,
		Metadata:    req.Metadata,
	}

	return mapGRPCError("VerifyFactor", grpcpkg.VerifyFactorUnary(ctx, t.conn, grpcReq))
}

// ListFactors lists enrolled auth factors via gRPC.
func (t *Transport) ListFactors(ctx context.Context, req *transport.ListFactorsRequest) (*transport.ListFactorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	grpcReq := &grpcpkg.GRPCListFactorsRequest{}
	if req != nil {
		grpcReq.Subject = req.Subject
	}

	resp, err := grpcpkg.ListFactorsUnary(ctx, t.conn, grpcReq)
	if err != nil {
		return nil, mapGRPCError("ListFactors", err)
	}

	entries := make([]transport.EnrollmentEntry, len(resp.Enrollments))
	for i, e := range resp.Enrollments {
		entries[i] = transport.EnrollmentEntry{
			EnrollmentID: e.EnrollmentID,
			FactorName:   e.FactorName,
			UserID:       e.UserID,
			TenantID:     e.TenantID,
		}
		entries[i].EnrolledAt, _ = time.Parse(time.RFC3339, e.EnrolledAt)
		if e.RevokedAt != "" {
			revokedAt, _ := time.Parse(time.RFC3339, e.RevokedAt)
			entries[i].RevokedAt = &revokedAt
		}
	}

	return &transport.ListFactorsResponse{
		Enrollments: entries,
		Total:       resp.Total,
	}, nil
}

// RevokeFactor permanently revokes an enrollment via gRPC.
func (t *Transport) RevokeFactor(ctx context.Context, req *transport.RevokeFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	grpcReq := &grpcpkg.GRPCRevokeFactorRequest{
		EnrollmentID: req.EnrollmentID,
	}

	return mapGRPCError("RevokeFactor", grpcpkg.RevokeFactorUnary(ctx, t.conn, grpcReq))
}
