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
	"encoding/json"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
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

	pbReq := &pb.EnrollFactorRequest{
		FactorName: req.FactorName,
		Subject:    req.Subject,
		TenantId:   req.TenantID,
	}
	if len(req.MethodParams) > 0 {
		pbReq.MethodParams = make(map[string]string, len(req.MethodParams))
		for k, v := range req.MethodParams {
			pbReq.MethodParams[k] = anyToString(v)
		}
	}

	resp, err := pb.NewAuthFactorServiceClient(t.conn).EnrollFactor(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("EnrollFactor", err)
	}

	enrolledAt, _ := time.Parse(time.RFC3339, resp.GetEnrolledAt())
	return &transport.EnrollFactorResponse{
		EnrollmentID: resp.GetEnrollmentId(),
		FactorName:   resp.GetFactorName(),
		UserID:       resp.GetUserId(),
		TenantID:     resp.GetTenantId(),
		EnrolledAt:   enrolledAt,
	}, nil
}

// ChallengeFactor issues a challenge for a previously enrolled factor via gRPC.
func (t *Transport) ChallengeFactor(ctx context.Context, req *transport.ChallengeFactorRequest) (*transport.ChallengeFactorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := pb.NewAuthFactorServiceClient(t.conn).ChallengeFactor(ctx, &pb.ChallengeFactorRequest{
		EnrollmentId: req.EnrollmentID,
	})
	if err != nil {
		return nil, mapGRPCError("ChallengeFactor", err)
	}

	expiresAt, _ := time.Parse(time.RFC3339, resp.GetExpiresAt())
	methodData := make(map[string]any, len(resp.GetMethodData()))
	for k, v := range resp.GetMethodData() {
		methodData[k] = v
	}
	return &transport.ChallengeFactorResponse{
		ChallengeID:  resp.GetChallengeId(),
		EnrollmentID: resp.GetEnrollmentId(),
		Nonce:        resp.GetNonce(),
		ExpiresAt:    expiresAt,
		MethodData:   methodData,
	}, nil
}

// VerifyFactor verifies the custodian's challenge response via gRPC.
func (t *Transport) VerifyFactor(ctx context.Context, req *transport.VerifyFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := pb.NewAuthFactorServiceClient(t.conn).VerifyFactor(ctx, &pb.VerifyFactorRequest{
		FactorName:  req.FactorName,
		ChallengeId: req.ChallengeID,
		Value:       req.Value,
		Metadata:    req.Metadata,
	}); err != nil {
		return mapGRPCError("VerifyFactor", err)
	}
	return nil
}

// ListFactors lists enrolled auth factors via gRPC.
func (t *Transport) ListFactors(ctx context.Context, req *transport.ListFactorsRequest) (*transport.ListFactorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.ListFactorsRequest{}
	if req != nil {
		pbReq.Subject = req.Subject
	}

	resp, err := pb.NewAuthFactorServiceClient(t.conn).ListFactors(ctx, pbReq)
	if err != nil {
		return nil, mapGRPCError("ListFactors", err)
	}

	entries := make([]transport.EnrollmentEntry, len(resp.GetEnrollments()))
	for i, e := range resp.GetEnrollments() {
		entries[i] = transport.EnrollmentEntry{
			EnrollmentID: e.GetEnrollmentId(),
			FactorName:   e.GetFactorName(),
			UserID:       e.GetUserId(),
			TenantID:     e.GetTenantId(),
		}
		entries[i].EnrolledAt, _ = time.Parse(time.RFC3339, e.GetEnrolledAt())
		if rv := e.GetRevokedAt(); rv != "" {
			revokedAt, _ := time.Parse(time.RFC3339, rv)
			entries[i].RevokedAt = &revokedAt
		}
	}

	return &transport.ListFactorsResponse{
		Enrollments: entries,
		Total:       int(resp.GetTotal()),
	}, nil
}

// RevokeFactor permanently revokes an enrollment via gRPC.
func (t *Transport) RevokeFactor(ctx context.Context, req *transport.RevokeFactorRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	if _, err := pb.NewAuthFactorServiceClient(t.conn).RevokeFactor(ctx, &pb.RevokeFactorRequest{
		EnrollmentId: req.EnrollmentID,
	}); err != nil {
		return mapGRPCError("RevokeFactor", err)
	}
	return nil
}

// anyToString converts any value to a string for protobuf map<string,string>
// fields. Strings are returned verbatim; all other types are JSON-encoded.
func anyToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
