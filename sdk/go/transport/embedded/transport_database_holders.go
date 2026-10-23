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
	ctx, err := t.admit(ctx, "DatabaseBarrierOfficerRegister")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DatabaseBarrierOfficerRegister"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRegister"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierOfficerRegister", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierOfficerRegister", err)
	}
	result, err := hs.DatabaseBarrierOfficerRegister(ctx, req.Database, holderwire.RegisterFrom(&req.OfficerRegisterRequest))
	if err != nil {
		return nil, delayed("DatabaseBarrierOfficerRegister", err)
	}
	return holderwire.RegisterTo(result), nil
}

// DatabaseBarrierOfficerList lists a database barrier's holders.
func (t *Transport) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierOfficerList")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DatabaseBarrierOfficerList"); err != nil {
		return nil, err
	}
	hs, err := t.databaseHolders("DatabaseBarrierOfficerList", name)
	if err != nil {
		return nil, delayed("DatabaseBarrierOfficerList", err)
	}
	officers, err := hs.DatabaseBarrierOfficerList(ctx, name)
	if err != nil {
		return nil, delayed("DatabaseBarrierOfficerList", err)
	}
	return holderwire.StatusesTo(officers), nil
}

// DatabaseBarrierShamirInitialize initializes a database barrier's custody.
func (t *Transport) DatabaseBarrierShamirInitialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierShamirInitialize")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DatabaseBarrierShamirInitialize"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShamirInitialize"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierShamirInitialize", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierShamirInitialize", err)
	}
	info, err := hs.DatabaseBarrierShamirInitialize(ctx, req.Database, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, delayed("DatabaseBarrierShamirInitialize", err)
	}
	return &transport.BarrierInitializeShamirResponse{Threshold: info.Threshold, TotalShares: info.TotalShares, ShareCount: info.ShareCount}, nil
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder.
// A ctx carrying no TLS certificate is an in-process claim, bound to the
// holder's enrolled certificate (claimContext).
func (t *Transport) DatabaseBarrierShareClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierShareClaimBegin")
	if err != nil {
		return nil, err
	}
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimBegin"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierShareClaimBegin", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierShareClaimBegin", err)
	}
	if err := t.limitHolderAct(ctx, "DatabaseBarrierShareClaimBegin", databaseScopeKey(ctx, req.Database)); err != nil {
		return nil, err
	}
	ctx, thumbprint := claimContext(ctx)
	challenge, err := hs.DatabaseBarrierShareClaimBegin(ctx, req.Database, req.Username, thumbprint)
	if err != nil {
		return nil, delayed("DatabaseBarrierShareClaimBegin", err)
	}
	return &transport.ShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt}, nil
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope
// exactly once.
func (t *Transport) DatabaseBarrierShareClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierShareClaimComplete")
	if err != nil {
		return nil, err
	}
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimComplete"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierShareClaimComplete", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierShareClaimComplete", err)
	}
	if err := t.limitHolderAct(ctx, "DatabaseBarrierShareClaimComplete", databaseScopeKey(ctx, req.Database)); err != nil {
		return nil, err
	}
	ctx, thumbprint := claimContext(ctx)
	result, err := hs.DatabaseBarrierShareClaimComplete(ctx, req.Database, req.Username, req.NonceHex, req.Signature, thumbprint)
	if err != nil {
		return nil, delayed("DatabaseBarrierShareClaimComplete", err)
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: result.ShareIndex, Envelope: result.Envelope}, nil
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
func (t *Transport) DatabaseBarrierQuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierQuorumBegin")
	if err != nil {
		return nil, err
	}
	// IdentityGated: no RBAC PERMISSION gates a quorum challenge -- a
	// holder's BINDING authorization is the factor it presents -- but an
	// authenticated caller is required, rate limited and audited, exactly
	// as permission.IdentityGated is on every network transport. ctx
	// carries no claimContext marking: that marker serves the share-CLAIM
	// handshake's certificate-binding fallback, which the quorum handshake
	// never reads.
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumBegin"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierQuorumBegin", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierQuorumBegin", err)
	}
	challenge, err := hs.DatabaseBarrierQuorumBegin(ctx, req.Database, holderwire.BeginFrom(&req.QuorumBeginRequest))
	if err != nil {
		return nil, delayed("DatabaseBarrierQuorumBegin", err)
	}
	return holderwire.ChallengeTo(challenge), nil
}

// DatabaseBarrierQuorumSubmit submits a database holder's share.
func (t *Transport) DatabaseBarrierQuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierQuorumSubmit")
	if err != nil {
		return nil, err
	}
	// IdentityGated -- see DatabaseBarrierQuorumBegin's doc comment.
	if err := t.requireIdentity(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumSubmit"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierQuorumSubmit", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierQuorumSubmit", err)
	}
	sub := holderwire.SubmitFrom(&req.QuorumSubmitRequest, quorumThumbprint(ctx))
	defer secure.Wipe(sub.Share)
	out, err := hs.DatabaseBarrierQuorumSubmit(ctx, req.Database, sub)
	if err != nil {
		return nil, delayed("DatabaseBarrierQuorumSubmit", err)
	}
	return holderwire.OutcomeTo(out), nil
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (t *Transport) DatabaseBarrierRekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierRekeyStart")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DatabaseBarrierRekeyStart"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierRekeyStart"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierRekeyStart", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierRekeyStart", err)
	}
	round, err := hs.DatabaseBarrierRekeyStart(ctx, req.Database, req.Threshold, seal.FactorPolicy(req.Policy))
	if err != nil {
		return nil, delayed("DatabaseBarrierRekeyStart", err)
	}
	return holderwire.RoundTo(round), nil
}

// DatabaseBarrierOfficerRevoke revokes a database holder.
func (t *Transport) DatabaseBarrierOfficerRevoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	ctx, err := t.admit(ctx, "DatabaseBarrierOfficerRevoke")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DatabaseBarrierOfficerRevoke"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRevoke"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierOfficerRevoke", req.Database)
	if err != nil {
		return nil, delayed("DatabaseBarrierOfficerRevoke", err)
	}
	round, err := hs.DatabaseBarrierOfficerRevoke(ctx, req.Database, req.Username, req.Threshold)
	if err != nil {
		return nil, delayed("DatabaseBarrierOfficerRevoke", err)
	}
	return holderwire.RoundTo(round), nil
}

// DatabaseBarrierOfficerUnenrol removes a database holder before the
// database's custody is initialized.
func (t *Transport) DatabaseBarrierOfficerUnenrol(ctx context.Context, req *transport.DatabaseOfficerUnenrolRequest) error {
	ctx, err := t.admit(ctx, "DatabaseBarrierOfficerUnenrol")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "DatabaseBarrierOfficerUnenrol"); err != nil {
		return err
	}
	if req == nil {
		return &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerUnenrol"}
	}
	hs, err := t.databaseHolders("DatabaseBarrierOfficerUnenrol", req.Database)
	if err != nil {
		return delayed("DatabaseBarrierOfficerUnenrol", err)
	}
	if err := hs.DatabaseBarrierOfficerUnenrol(ctx, req.Database, req.Username); err != nil {
		return delayed("DatabaseBarrierOfficerUnenrol", err)
	}
	return nil
}
