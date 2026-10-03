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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
)

// Shamir custody of a database barrier (docs/databases/api.md), over the
// same QRDBService wire the gRPC transport serves; the calls are
// holderconv.DatabaseHolders'.

// databaseHolders binds the database share-holder calls to this transport's
// client and error mapping.
func (t *Transport) databaseHolders() holderconv.DatabaseHolders {
	return holderconv.DatabaseHolders{Client: t.client, MapErr: mapGRPCError}
}

// DatabaseBarrierOfficerRegister enrols a share holder of a database's barrier.
func (t *Transport) DatabaseBarrierOfficerRegister(ctx context.Context, req *transport.DatabaseOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().Register(ctx, req)
}

// DatabaseBarrierOfficerList lists a database barrier's holders.
func (t *Transport) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().List(ctx, name)
}

// DatabaseBarrierShamirInitialize splits a database's root key to its enrolled holders.
func (t *Transport) DatabaseBarrierShamirInitialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().Initialize(ctx, req)
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder presenting its certificate.
func (t *Transport) DatabaseBarrierShareClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().ClaimBegin(ctx, req)
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope exactly once.
func (t *Transport) DatabaseBarrierShareClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().ClaimComplete(ctx, req)
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
func (t *Transport) DatabaseBarrierQuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().QuorumBegin(ctx, req)
}

// DatabaseBarrierQuorumSubmit submits a database holder's share.
func (t *Transport) DatabaseBarrierQuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().QuorumSubmit(ctx, req)
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (t *Transport) DatabaseBarrierRekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().RekeyStart(ctx, req)
}

// DatabaseBarrierOfficerRevoke revokes a database holder.
func (t *Transport) DatabaseBarrierOfficerRevoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().Revoke(ctx, req)
}

// DatabaseBarrierOfficerUnenrol removes a database holder before the
// database's custody is initialized.
func (t *Transport) DatabaseBarrierOfficerUnenrol(ctx context.Context, req *transport.DatabaseOfficerUnenrolRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.databaseHolders().Unenrol(ctx, req)
}
