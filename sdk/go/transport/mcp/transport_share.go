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

// Compile-time interface assertion.
var _ transport.ShareClient = (*Transport)(nil)

// SubmitShare submits a received Shamir share back to the server. This method
// is not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) SubmitShare(ctx context.Context, req *transport.SubmitShareRequest) (*transport.SubmitShareResponse, error) {
	return nil, &transport.NotImplementedError{Method: "SubmitShare"}
}

// ListShares lists shares available for the authenticated user. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) ListShares(ctx context.Context) (*transport.ListSharesResponse, error) {
	return nil, &transport.NotImplementedError{Method: "ListShares"}
}

// GetShare retrieves a single submitted share. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) GetShare(ctx context.Context, serverURL, groupID string, shareIndex int) (*transport.ShareInfo, error) {
	return nil, &transport.NotImplementedError{Method: "GetShare"}
}

// DeleteShare removes a single submitted share. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) DeleteShare(ctx context.Context, serverURL, groupID string, shareIndex int) error {
	return &transport.NotImplementedError{Method: "DeleteShare"}
}

// GetShareCollectionStatus returns the collection status for a group. This
// method is not yet implemented for the MCP transport and requires
// corresponding MCP tool definitions.
func (t *Transport) GetShareCollectionStatus(ctx context.Context, groupID string) (*transport.ShareCollectionStatus, error) {
	return nil, &transport.NotImplementedError{Method: "GetShareCollectionStatus"}
}
