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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// CeremonyClaimShareBegin issues a factor challenge. Maps to
// POST /v1/ceremony/claim-share/begin.
func (t *Transport) CeremonyClaimShareBegin(ctx context.Context, req *transport.CeremonyClaimShareBeginRequest) (*transport.CeremonyClaimShareBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.CeremonyClaimShareBeginResponse
	if err := t.postJSONDecode(ctx, "CeremonyClaimShareBegin", "/v1/ceremony/claim-share/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CeremonyClaimShareComplete verifies the factor response. Maps to
// POST /v1/ceremony/claim-share/complete.
func (t *Transport) CeremonyClaimShareComplete(ctx context.Context, req *transport.CeremonyClaimShareCompleteRequest) (*transport.CeremonyCustodianInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.CeremonyCustodianInfo
	if err := t.postJSONDecode(ctx, "CeremonyClaimShareComplete", "/v1/ceremony/claim-share/complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CeremonySubmitUnsealShare submits a share toward the quorum. Maps to
// POST /v1/barrier/unseal-share.
func (t *Transport) CeremonySubmitUnsealShare(ctx context.Context, req *transport.CeremonySubmitUnsealShareRequest) (*transport.CeremonySubmitUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.CeremonySubmitUnsealShareResponse
	if err := t.postJSONDecode(ctx, "CeremonySubmitUnsealShare", "/v1/barrier/unseal-share", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
