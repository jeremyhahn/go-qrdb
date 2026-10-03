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

package transport

import "context"

// The share-holder operations of a tenant-owned database's barrier. Each
// verifies the database belongs to this tenant before it is forwarded, as
// every other per-database operation does; a nil request names no database
// and is refused.

// DatabaseBarrierOfficerRegister enrols a holder of a tenant-owned database.
func (t *TenantDatabaseClient) DatabaseBarrierOfficerRegister(ctx context.Context, req *DatabaseOfficerRegisterRequest) (*OfficerRegisterResponse, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRegister"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierOfficerRegister(ctx, req)
}

// DatabaseBarrierOfficerList lists a tenant-owned database's holders.
func (t *TenantDatabaseClient) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*OfficerStatus, error) {
	if err := t.verifyOwnership(ctx, name); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierOfficerList(ctx, name)
}

// DatabaseBarrierShamirInitialize initializes a tenant-owned database's
// custody.
func (t *TenantDatabaseClient) DatabaseBarrierShamirInitialize(ctx context.Context, req *DatabaseShamirInitializeRequest) (*BarrierInitializeShamirResponse, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierShamirInitialize"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierShamirInitialize(ctx, req)
}

// DatabaseBarrierShareClaimBegin opens a claim on a tenant-owned database.
func (t *TenantDatabaseClient) DatabaseBarrierShareClaimBegin(ctx context.Context, req *DatabaseShareClaimBeginRequest) (*ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimBegin"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierShareClaimBegin(ctx, req)
}

// DatabaseBarrierShareClaimComplete completes a claim on a tenant-owned
// database.
func (t *TenantDatabaseClient) DatabaseBarrierShareClaimComplete(ctx context.Context, req *DatabaseShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimComplete"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierShareClaimComplete(ctx, req)
}

// DatabaseBarrierQuorumBegin issues a quorum challenge on a tenant-owned
// database.
func (t *TenantDatabaseClient) DatabaseBarrierQuorumBegin(ctx context.Context, req *DatabaseQuorumBeginRequest) (*QuorumChallenge, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumBegin"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierQuorumBegin(ctx, req)
}

// DatabaseBarrierQuorumSubmit submits a share on a tenant-owned database.
func (t *TenantDatabaseClient) DatabaseBarrierQuorumSubmit(ctx context.Context, req *DatabaseQuorumSubmitRequest) (*QuorumOutcome, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumSubmit"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierQuorumSubmit(ctx, req)
}

// DatabaseBarrierRekeyStart opens a rekey round on a tenant-owned database.
func (t *TenantDatabaseClient) DatabaseBarrierRekeyStart(ctx context.Context, req *DatabaseRekeyStartRequest) (*QuorumRound, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierRekeyStart"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierRekeyStart(ctx, req)
}

// DatabaseBarrierOfficerRevoke revokes a holder of a tenant-owned database.
func (t *TenantDatabaseClient) DatabaseBarrierOfficerRevoke(ctx context.Context, req *DatabaseOfficerRevokeRequest) (*QuorumRound, error) {
	if req == nil {
		return nil, &DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRevoke"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return nil, err
	}
	return t.inner.DatabaseBarrierOfficerRevoke(ctx, req)
}

// DatabaseBarrierOfficerUnenrol removes a holder of a tenant-owned database
// before its custody is initialized.
func (t *TenantDatabaseClient) DatabaseBarrierOfficerUnenrol(ctx context.Context, req *DatabaseOfficerUnenrolRequest) error {
	if req == nil {
		return &DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerUnenrol"}
	}
	if err := t.verifyOwnership(ctx, req.Database); err != nil {
		return err
	}
	return t.inner.DatabaseBarrierOfficerUnenrol(ctx, req)
}
