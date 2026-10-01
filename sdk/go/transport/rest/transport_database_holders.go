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

package rest

import (
	"context"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// databaseHolderPath is /v1/databases/{name}/barrier{suffix}.
func databaseHolderPath(method, name, suffix string) (string, error) {
	if name == "" {
		return "", &transport.DatabaseNameRequiredError{Method: method}
	}
	return "/v1/databases/" + url.PathEscape(name) + "/barrier" + suffix, nil
}

// databaseHolderPost posts payload to the database barrier path for suffix
// and decodes the answer into dest.
func (t *Transport) databaseHolderPost(ctx context.Context, method, name, suffix string, payload, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	path, err := databaseHolderPath(method, name, suffix)
	if err != nil {
		return err
	}
	return t.postJSONDecode(ctx, method, path, payload, dest)
}

// DatabaseBarrierOfficerRegister enrols a share holder of a database's
// barrier. Maps to POST /v1/databases/{name}/barrier/officers.
func (t *Transport) DatabaseBarrierOfficerRegister(ctx context.Context, req *transport.DatabaseOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRegister"}
	}
	var resp transport.OfficerRegisterResponse
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierOfficerRegister", req.Database, "/officers", &req.OfficerRegisterRequest, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierOfficerList lists a database barrier's holders. Maps to
// GET /v1/databases/{name}/barrier/officers.
func (t *Transport) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	path, err := databaseHolderPath("DatabaseBarrierOfficerList", name, "/officers")
	if err != nil {
		return nil, err
	}
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if err := t.getJSONDecode(ctx, "DatabaseBarrierOfficerList", path, &wire); err != nil {
		return nil, err
	}
	return wire.Officers, nil
}

// DatabaseBarrierShamirInitialize initializes a database barrier's custody.
// Maps to POST /v1/databases/{name}/barrier/shamir/initialize.
func (t *Transport) DatabaseBarrierShamirInitialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShamirInitialize"}
	}
	var resp transport.BarrierInitializeShamirResponse
	payload := map[string]string{"policy": req.Policy}
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierShamirInitialize", req.Database, "/shamir/initialize", payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder
// presenting its certificate. Maps to
// POST /v1/databases/{name}/barrier/officers/{username}/claim/begin.
func (t *Transport) DatabaseBarrierShareClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimBegin"}
	}
	var resp transport.ShareClaimBeginResponse
	suffix := "/officers/" + url.PathEscape(req.Username) + "/claim/begin"
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierShareClaimBegin", req.Database, suffix, struct{}{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope
// exactly once. Maps to
// POST /v1/databases/{name}/barrier/officers/{username}/claim/complete.
func (t *Transport) DatabaseBarrierShareClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimComplete"}
	}
	var resp transport.ShareClaimCompleteResponse
	suffix := "/officers/" + url.PathEscape(req.Username) + "/claim/complete"
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierShareClaimComplete", req.Database, suffix, &req.ShareClaimCompleteRequest, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
// Maps to POST /v1/databases/{name}/barrier/quorum/begin.
func (t *Transport) DatabaseBarrierQuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumBegin"}
	}
	var resp transport.QuorumChallenge
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierQuorumBegin", req.Database, "/quorum/begin", &req.QuorumBeginRequest, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierQuorumSubmit submits a database holder's share. Maps to
// POST /v1/databases/{name}/barrier/quorum/submit.
func (t *Transport) DatabaseBarrierQuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumSubmit"}
	}
	var resp transport.QuorumOutcome
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierQuorumSubmit", req.Database, "/quorum/submit", &req.QuorumSubmitRequest, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier. Maps
// to POST /v1/databases/{name}/barrier/rekey/start.
func (t *Transport) DatabaseBarrierRekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierRekeyStart"}
	}
	var resp transport.QuorumRound
	payload := map[string]any{"threshold": req.Threshold, "policy": req.Policy}
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierRekeyStart", req.Database, "/rekey/start", payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DatabaseBarrierOfficerRevoke revokes a database holder. Maps to
// POST /v1/databases/{name}/barrier/officers/revoke.
func (t *Transport) DatabaseBarrierOfficerRevoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRevoke"}
	}
	var resp transport.QuorumRound
	payload := map[string]any{"username": req.Username, "threshold": req.Threshold}
	if err := t.databaseHolderPost(ctx, "DatabaseBarrierOfficerRevoke", req.Database, "/officers/revoke", payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
