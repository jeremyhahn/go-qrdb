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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// CeremonyClaimShareBegin issues a factor challenge for a custodian to claim
// their Shamir share.
func (s *qrdbServiceServer) CeremonyClaimShareBegin(ctx context.Context, req *pb.CeremonyClaimShareBeginRequest) (*pb.CeremonyClaimShareBeginResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}
	ch, err := s.ceremonySvc.CeremonyClaimShareBegin(ctx, req.GetCustodianId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.CeremonyClaimShareBeginResponse{
		CustodianId:   ch.CustodianID,
		Nonce:         ch.Nonce,
		ExpiresAtUnix: ch.ExpiresAt.Unix(),
	}, nil
}

// CeremonyClaimShareComplete verifies the custodian's factor response and
// returns the custodian metadata confirming delivery.
func (s *qrdbServiceServer) CeremonyClaimShareComplete(ctx context.Context, req *pb.CeremonyClaimShareCompleteRequest) (*pb.CeremonyCustodianResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}
	c, err := s.ceremonySvc.CeremonyClaimShareComplete(ctx, req.GetCustodianId(), req.GetChallengeId(), req.GetFactorResponse())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return custodianToProto(c), nil
}

// CeremonySubmitUnsealShare submits a custodian's plaintext share plus factor
// proof toward the unseal quorum.
func (s *qrdbServiceServer) CeremonySubmitUnsealShare(ctx context.Context, req *pb.CeremonySubmitUnsealShareRequest) (*pb.CeremonySubmitUnsealShareResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}
	result, err := s.ceremonySvc.CeremonySubmitUnsealShare(ctx, req.GetCustodianId(), req.GetChallengeId(), req.GetFactorResponse(), req.GetShareBytes())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.CeremonySubmitUnsealShareResponse{
		Submitted: int32(result.Submitted),
		Required:  int32(result.Required),
		Unsealed:  result.Unsealed,
	}, nil
}

// custodianToProto converts a custodian.Custodian to its protobuf form. The
// optional RevokedAt/ShareClaimedAt timestamps are encoded as 0 when unset.
func custodianToProto(c *custodian.Custodian) *pb.CeremonyCustodianResponse {
	if c == nil {
		return &pb.CeremonyCustodianResponse{}
	}
	resp := &pb.CeremonyCustodianResponse{
		Id:             c.ID,
		Username:       c.Username,
		TenantId:       c.TenantID,
		Method:         string(c.Method),
		EnrollmentId:   c.EnrollmentID,
		PublicKeyPem:   c.PublicKeyPEM,
		ShareIndex:     int32(c.ShareIndex),
		EnrolledAtUnix: c.EnrolledAt.Unix(),
		ShareClaimed:   c.ShareClaimed,
	}
	if c.RevokedAt != nil {
		resp.RevokedAtUnix = c.RevokedAt.Unix()
	}
	if c.ShareClaimedAt != nil {
		resp.ShareClaimedAtUnix = c.ShareClaimedAt.Unix()
	}
	return resp
}
