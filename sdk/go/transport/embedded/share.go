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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SubmitShare submits a received Shamir share back to the server.
func (t *Transport) SubmitShare(ctx context.Context, req *transport.SubmitShareRequest) (*transport.SubmitShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.shareSvc == nil {
		return nil, &NilServiceError{Service: "ShareServicer"}
	}
	return t.shareSvc.SubmitShare(ctx, req)
}

// ListShares lists shares available for the authenticated user.
func (t *Transport) ListShares(ctx context.Context) (*transport.ListSharesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.shareSvc == nil {
		return nil, &NilServiceError{Service: "ShareServicer"}
	}
	shares, err := t.shareSvc.ListShares(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.ListSharesResponse{Shares: shares}, nil
}

// GetShareCollectionStatus returns the collection status for a group.
func (t *Transport) GetShareCollectionStatus(ctx context.Context, groupID string) (*transport.ShareCollectionStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.shareSvc == nil {
		return nil, &NilServiceError{Service: "ShareServicer"}
	}
	return t.shareSvc.GetShareCollectionStatus(ctx, groupID)
}

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// WithShareService injects a ShareServicer into the transport.
func WithShareService(svc ceremony.ShareServicer) Option {
	return func(t *Transport) {
		t.shareSvc = svc
	}
}
