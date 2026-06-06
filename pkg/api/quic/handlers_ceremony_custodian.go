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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// handleCeremonyClaimShareBegin issues a factor challenge for a custodian to
// claim their Shamir share.
func (s *Server) handleCeremonyClaimShareBegin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}
	var req pb.CeremonyClaimShareBeginRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	ch, err := s.ceremonySvc.CeremonyClaimShareBegin(ctx, req.GetCustodianId())
	if err != nil {
		return nil, err
	}
	out, err := proto.Marshal(&pb.CeremonyClaimShareBeginResponse{
		CustodianId:   ch.CustodianID,
		Nonce:         ch.Nonce,
		ExpiresAtUnix: ch.ExpiresAt.Unix(),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleCeremonyClaimShareComplete verifies the custodian's factor response and
// returns the custodian metadata confirming delivery.
func (s *Server) handleCeremonyClaimShareComplete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}
	var req pb.CeremonyClaimShareCompleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	c, err := s.ceremonySvc.CeremonyClaimShareComplete(ctx, req.GetCustodianId(), req.GetChallengeId(), req.GetFactorResponse())
	if err != nil {
		return nil, err
	}
	out, err := proto.Marshal(custodianToProto(c))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleCeremonySubmitUnsealShare submits a custodian's plaintext share plus
// factor proof toward the unseal quorum.
func (s *Server) handleCeremonySubmitUnsealShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}
	var req pb.CeremonySubmitUnsealShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	result, err := s.ceremonySvc.CeremonySubmitUnsealShare(ctx, req.GetCustodianId(), req.GetChallengeId(), req.GetFactorResponse(), req.GetShareBytes())
	if err != nil {
		return nil, err
	}
	out, err := proto.Marshal(&pb.CeremonySubmitUnsealShareResponse{
		Submitted: int32(result.Submitted),
		Required:  int32(result.Required),
		Unsealed:  result.Unsealed,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// custodianToProto converts a custodian.Custodian to its protobuf form, with
// optional timestamps encoded as 0 when unset.
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
