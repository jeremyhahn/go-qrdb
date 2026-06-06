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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CeremonyClaimShareBegin issues a factor challenge via the QUIC handler.
func (t *Transport) CeremonyClaimShareBegin(ctx context.Context, req *transport.CeremonyClaimShareBeginRequest) (*transport.CeremonyClaimShareBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.CeremonyClaimShareBeginRequest{CustodianId: req.CustodianID})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareBegin", Err: err}
	}
	respBytes, err := t.call(ctx, "ceremony.ClaimShareBegin", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareBegin", Err: err}
	}
	var resp pb.CeremonyClaimShareBeginResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareBegin", Err: err}
	}
	return &transport.CeremonyClaimShareBeginResponse{
		CustodianID: resp.GetCustodianId(),
		Nonce:       resp.GetNonce(),
		ExpiresAt:   time.Unix(resp.GetExpiresAtUnix(), 0),
	}, nil
}

// CeremonyClaimShareComplete verifies the factor response via the QUIC handler.
func (t *Transport) CeremonyClaimShareComplete(ctx context.Context, req *transport.CeremonyClaimShareCompleteRequest) (*transport.CeremonyCustodianInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.CeremonyClaimShareCompleteRequest{
		CustodianId:    req.CustodianID,
		ChallengeId:    req.ChallengeID,
		FactorResponse: req.FactorResponse,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareComplete", Err: err}
	}
	respBytes, err := t.call(ctx, "ceremony.ClaimShareComplete", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareComplete", Err: err}
	}
	var resp pb.CeremonyCustodianResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CeremonyClaimShareComplete", Err: err}
	}
	return quicCustodianToInfo(&resp), nil
}

// CeremonySubmitUnsealShare submits a share toward the quorum via the QUIC handler.
func (t *Transport) CeremonySubmitUnsealShare(ctx context.Context, req *transport.CeremonySubmitUnsealShareRequest) (*transport.CeremonySubmitUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.CeremonySubmitUnsealShareRequest{
		CustodianId:    req.CustodianID,
		ChallengeId:    req.ChallengeID,
		FactorResponse: req.FactorResponse,
		ShareBytes:     req.ShareBytes,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonySubmitUnsealShare", Err: err}
	}
	respBytes, err := t.call(ctx, "ceremony.SubmitUnsealShare", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CeremonySubmitUnsealShare", Err: err}
	}
	var resp pb.CeremonySubmitUnsealShareResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "CeremonySubmitUnsealShare", Err: err}
	}
	return &transport.CeremonySubmitUnsealShareResponse{
		Submitted: int(resp.GetSubmitted()),
		Required:  int(resp.GetRequired()),
		Unsealed:  resp.GetUnsealed(),
	}, nil
}

// quicCustodianToInfo converts a proto CeremonyCustodianResponse to the
// transport contract, decoding unix timestamps (0 = unset for optionals).
func quicCustodianToInfo(c *pb.CeremonyCustodianResponse) *transport.CeremonyCustodianInfo {
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
