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

package unix

import (
	"context"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CeremonyClaimShareBegin issues a factor challenge via the gRPC RPC over UDS.
func (t *Transport) CeremonyClaimShareBegin(ctx context.Context, req *transport.CeremonyClaimShareBeginRequest) (*transport.CeremonyClaimShareBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyClaimShareBegin(ctx, &pb.CeremonyClaimShareBeginRequest{CustodianId: req.CustodianID})
	if err != nil {
		return nil, mapGRPCError("CeremonyClaimShareBegin", err)
	}
	return &transport.CeremonyClaimShareBeginResponse{
		CustodianID: resp.GetCustodianId(),
		Nonce:       resp.GetNonce(),
		ExpiresAt:   time.Unix(resp.GetExpiresAtUnix(), 0),
	}, nil
}

// CeremonyClaimShareComplete verifies the factor response via the gRPC RPC.
func (t *Transport) CeremonyClaimShareComplete(ctx context.Context, req *transport.CeremonyClaimShareCompleteRequest) (*transport.CeremonyCustodianInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyClaimShareComplete(ctx, &pb.CeremonyClaimShareCompleteRequest{
		CustodianId:    req.CustodianID,
		ChallengeId:    req.ChallengeID,
		FactorResponse: req.FactorResponse,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyClaimShareComplete", err)
	}
	return protoCustodianToInfo(resp), nil
}

// CeremonySubmitUnsealShare submits a share toward the quorum via the gRPC RPC.
func (t *Transport) CeremonySubmitUnsealShare(ctx context.Context, req *transport.CeremonySubmitUnsealShareRequest) (*transport.CeremonySubmitUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonySubmitUnsealShare(ctx, &pb.CeremonySubmitUnsealShareRequest{
		CustodianId:    req.CustodianID,
		ChallengeId:    req.ChallengeID,
		FactorResponse: req.FactorResponse,
		ShareBytes:     req.ShareBytes,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonySubmitUnsealShare", err)
	}
	return &transport.CeremonySubmitUnsealShareResponse{
		Submitted: int(resp.GetSubmitted()),
		Required:  int(resp.GetRequired()),
		Unsealed:  resp.GetUnsealed(),
	}, nil
}

// protoCustodianToInfo converts a proto CeremonyCustodianResponse to the
// transport contract, decoding unix timestamps (0 = unset for optionals).
func protoCustodianToInfo(c *pb.CeremonyCustodianResponse) *transport.CeremonyCustodianInfo {
	if c == nil {
		return &transport.CeremonyCustodianInfo{}
	}
	info := &transport.CeremonyCustodianInfo{
		ID:           c.GetId(),
		Username:     c.GetUsername(),
		TenantID:     c.GetTenantId(),
		Method:       c.GetMethod(),
		EnrollmentID: c.GetEnrollmentId(),
		PublicKeyPEM: c.GetPublicKeyPem(),
		ShareIndex:   int(c.GetShareIndex()),
		EnrolledAt:   time.Unix(c.GetEnrolledAtUnix(), 0),
		ShareClaimed: c.GetShareClaimed(),
	}
	if c.GetRevokedAtUnix() != 0 {
		ts := time.Unix(c.GetRevokedAtUnix(), 0)
		info.RevokedAt = &ts
	}
	if c.GetShareClaimedAtUnix() != 0 {
		ts := time.Unix(c.GetShareClaimedAtUnix(), 0)
		info.ShareClaimedAt = &ts
	}
	return info
}
