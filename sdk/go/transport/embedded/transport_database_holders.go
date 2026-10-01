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

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// databaseHolders resolves the database service's share-holder custody for
// method, refusing a request that names no database.
func (t *Transport) databaseHolders(method, name string) (service.DatabaseHolderServicer, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if name == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: method}
	}
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	hs, ok := t.dbSvc.(service.DatabaseHolderServicer)
	if !ok {
		return nil, &NilServiceError{Service: "DatabaseHolderServicer"}
	}
	return hs, nil
}

// DatabaseBarrierOfficerRegister enrols a share holder of a database's
// barrier.
func (t *Transport) DatabaseBarrierOfficerRegister(ctx context.Context, req *transport.DatabaseOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRegister"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierOfficerRegister", req.Database)
	if err != nil {
		return nil, err
	}
	result, err := hs.DatabaseBarrierOfficerRegister(ctx, req.Database, holderwire.RegisterFrom(&req.OfficerRegisterRequest))
	if err != nil {
		return nil, err
	}
	return holderwire.RegisterTo(result), nil
}

// DatabaseBarrierOfficerList lists a database barrier's holders.
func (t *Transport) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	hs, err := t.databaseHolders("DatabaseBarrierOfficerList", name)
	if err != nil {
		return nil, err
	}
	officers, err := hs.DatabaseBarrierOfficerList(ctx, name)
	if err != nil {
		return nil, err
	}
	return holderwire.StatusesTo(officers), nil
}

// DatabaseBarrierShamirInitialize initializes a database barrier's custody.
func (t *Transport) DatabaseBarrierShamirInitialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShamirInitialize"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierShamirInitialize", req.Database)
	if err != nil {
		return nil, err
	}
	info, err := hs.DatabaseBarrierShamirInitialize(ctx, req.Database, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, err
	}
	return &transport.BarrierInitializeShamirResponse{Threshold: info.Threshold, TotalShares: info.TotalShares, ShareCount: info.ShareCount}, nil
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder.
// A ctx carrying no TLS certificate is an in-process claim, bound to the
// holder's enrolled certificate (claimContext).
func (t *Transport) DatabaseBarrierShareClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimBegin"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierShareClaimBegin", req.Database)
	if err != nil {
		return nil, err
	}
	ctx, thumbprint := claimContext(ctx)
	challenge, err := hs.DatabaseBarrierShareClaimBegin(ctx, req.Database, req.Username, thumbprint)
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt}, nil
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope
// exactly once.
func (t *Transport) DatabaseBarrierShareClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimComplete"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierShareClaimComplete", req.Database)
	if err != nil {
		return nil, err
	}
	ctx, thumbprint := claimContext(ctx)
	result, err := hs.DatabaseBarrierShareClaimComplete(ctx, req.Database, req.Username, req.NonceHex, req.Signature, thumbprint)
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
func (t *Transport) DatabaseBarrierQuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumBegin"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierQuorumBegin", req.Database)
	if err != nil {
		return nil, err
	}
	challenge, err := hs.DatabaseBarrierQuorumBegin(ctx, req.Database, holderwire.BeginFrom(&req.QuorumBeginRequest))
	if err != nil {
		return nil, err
	}
	return holderwire.ChallengeTo(challenge), nil
}

// DatabaseBarrierQuorumSubmit submits a database holder's share.
func (t *Transport) DatabaseBarrierQuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumSubmit"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierQuorumSubmit", req.Database)
	if err != nil {
		return nil, err
	}
	sub := holderwire.SubmitFrom(&req.QuorumSubmitRequest, contextOptionalThumbprint(ctx))
	defer secure.Wipe(sub.Share)
	out, err := hs.DatabaseBarrierQuorumSubmit(ctx, req.Database, sub)
	if err != nil {
		return nil, err
	}
	return holderwire.OutcomeTo(out), nil
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (t *Transport) DatabaseBarrierRekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierRekeyStart"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierRekeyStart", req.Database)
	if err != nil {
		return nil, err
	}
	round, err := hs.DatabaseBarrierRekeyStart(ctx, req.Database, req.Threshold, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, err
	}
	return holderwire.RoundTo(round), nil
}

// DatabaseBarrierOfficerRevoke revokes a database holder.
func (t *Transport) DatabaseBarrierOfficerRevoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRevoke"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierOfficerRevoke", req.Database)
	if err != nil {
		return nil, err
	}
	round, err := hs.DatabaseBarrierOfficerRevoke(ctx, req.Database, req.Username, req.Threshold)
	if err != nil {
		return nil, err
	}
	return holderwire.RoundTo(round), nil
}
