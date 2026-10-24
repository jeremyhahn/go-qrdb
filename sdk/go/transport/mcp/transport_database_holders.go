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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Shamir custody of a database barrier (docs/databases/api.md), over the
// qrdb_database_barrier_* tools. Each request's fields are the tool's
// arguments.

// callDatabaseHolder makes one database share-holder tool call. A request
// that names no database is refused before the call: there is no scope to
// address.
func callDatabaseHolder[Resp any](ctx context.Context, t *Transport, method, tool, database string, req any) (*Resp, error) {
	if database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: method}
	}
	var resp Resp
	if err := t.callHolderTool(ctx, method, tool, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierOfficerRegister enrols a share holder of a database's
// barrier.
func (t *Transport) DatabaseBarrierOfficerRegister(ctx context.Context, req *transport.DatabaseOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRegister"}
	}
	return callDatabaseHolder[transport.OfficerRegisterResponse](ctx, t, "DatabaseBarrierOfficerRegister", "qrdb_database_barrier_officer_register", req.Database, req)
}

// DatabaseBarrierOfficerList lists a database barrier's holders.
func (t *Transport) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	req := struct {
		Database string `json:"database"`
	}{Database: name}
	resp, err := callDatabaseHolder[struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}](ctx, t, "DatabaseBarrierOfficerList", "qrdb_database_barrier_officer_list", name, req)
	if err != nil {
		return nil, err
	}
	return resp.Officers, nil
}

// DatabaseBarrierShamirInitialize splits a database's root key to its
// enrolled holders.
func (t *Transport) DatabaseBarrierShamirInitialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShamirInitialize"}
	}
	return callDatabaseHolder[transport.BarrierInitializeShamirResponse](ctx, t, "DatabaseBarrierShamirInitialize", "qrdb_database_barrier_shamir_initialize", req.Database, req)
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder
// presenting its certificate.
func (t *Transport) DatabaseBarrierShareClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimBegin"}
	}
	return callDatabaseHolder[transport.ShareClaimBeginResponse](ctx, t, "DatabaseBarrierShareClaimBegin", "qrdb_database_barrier_share_claim_begin", req.Database, req)
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope
// exactly once.
func (t *Transport) DatabaseBarrierShareClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimComplete"}
	}
	return callDatabaseHolder[transport.ShareClaimCompleteResponse](ctx, t, "DatabaseBarrierShareClaimComplete", "qrdb_database_barrier_share_claim_complete", req.Database, req)
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
func (t *Transport) DatabaseBarrierQuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumBegin"}
	}
	return callDatabaseHolder[transport.QuorumChallenge](ctx, t, "DatabaseBarrierQuorumBegin", "qrdb_database_barrier_quorum_begin", req.Database, req)
}

// DatabaseBarrierQuorumSubmit submits a database holder's share.
func (t *Transport) DatabaseBarrierQuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	ctx, err := t.guardSecret(ctx, req)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumSubmit"}
	}
	return callDatabaseHolder[transport.QuorumOutcome](ctx, t, "DatabaseBarrierQuorumSubmit", "qrdb_database_barrier_quorum_submit", req.Database, req)
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (t *Transport) DatabaseBarrierRekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierRekeyStart"}
	}
	return callDatabaseHolder[transport.QuorumRound](ctx, t, "DatabaseBarrierRekeyStart", "qrdb_database_barrier_rekey_start", req.Database, req)
}

// DatabaseBarrierOfficerRevoke revokes a database holder.
func (t *Transport) DatabaseBarrierOfficerRevoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRevoke"}
	}
	return callDatabaseHolder[transport.QuorumRound](ctx, t, "DatabaseBarrierOfficerRevoke", "qrdb_database_barrier_officer_revoke", req.Database, req)
}

// DatabaseBarrierOfficerUnenrol removes a database holder before the
// database's custody is initialized.
func (t *Transport) DatabaseBarrierOfficerUnenrol(ctx context.Context, req *transport.DatabaseOfficerUnenrolRequest) error {
	if req == nil {
		return &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerUnenrol"}
	}
	_, err := callDatabaseHolder[struct {
		Unenrolled string `json:"unenrolled"`
	}](ctx, t, "DatabaseBarrierOfficerUnenrol", "qrdb_database_barrier_officer_unenrol", req.Database, req)
	return err
}
