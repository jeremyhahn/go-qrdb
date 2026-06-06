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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CeremonyClaimShareBegin issues a factor challenge via the injected service.
func (t *Transport) CeremonyClaimShareBegin(ctx context.Context, req *transport.CeremonyClaimShareBeginRequest) (*transport.CeremonyClaimShareBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	ch, err := t.ceremonySvc.CeremonyClaimShareBegin(ctx, req.CustodianID)
	if err != nil {
		return nil, err
	}
	return &transport.CeremonyClaimShareBeginResponse{
		CustodianID: ch.CustodianID,
		Nonce:       ch.Nonce,
		ExpiresAt:   ch.ExpiresAt,
	}, nil
}

// CeremonyClaimShareComplete verifies the factor response via the service.
func (t *Transport) CeremonyClaimShareComplete(ctx context.Context, req *transport.CeremonyClaimShareCompleteRequest) (*transport.CeremonyCustodianInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	c, err := t.ceremonySvc.CeremonyClaimShareComplete(ctx, req.CustodianID, req.ChallengeID, req.FactorResponse)
	if err != nil {
		return nil, err
	}
	return custodianToInfo(c), nil
}

// CeremonySubmitUnsealShare submits a share toward the quorum via the service.
func (t *Transport) CeremonySubmitUnsealShare(ctx context.Context, req *transport.CeremonySubmitUnsealShareRequest) (*transport.CeremonySubmitUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.ceremonySvc == nil {
		return nil, &NilServiceError{Service: "CeremonyServicer"}
	}
	result, err := t.ceremonySvc.CeremonySubmitUnsealShare(ctx, req.CustodianID, req.ChallengeID, req.FactorResponse, req.ShareBytes)
	if err != nil {
		return nil, err
	}
	return &transport.CeremonySubmitUnsealShareResponse{
		Submitted: result.Submitted,
		Required:  result.Required,
		Unsealed:  result.Unsealed,
	}, nil
}

// custodianToInfo converts a custodian.Custodian to the transport contract.
func custodianToInfo(c *custodian.Custodian) *transport.CeremonyCustodianInfo {
	if c == nil {
		return &transport.CeremonyCustodianInfo{}
	}
	return &transport.CeremonyCustodianInfo{
		ID:             c.ID,
		Username:       c.Username,
		TenantID:       c.TenantID,
		Method:         string(c.Method),
		EnrollmentID:   c.EnrollmentID,
		PublicKeyPEM:   c.PublicKeyPEM,
		ShareIndex:     c.ShareIndex,
		EnrolledAt:     c.EnrolledAt,
		RevokedAt:      c.RevokedAt,
		ShareClaimed:   c.ShareClaimed,
		ShareClaimedAt: c.ShareClaimedAt,
	}
}
