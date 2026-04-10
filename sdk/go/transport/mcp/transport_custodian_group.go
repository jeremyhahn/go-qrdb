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
var _ transport.CustodianGroupClient = (*Transport)(nil)

// CreateCustodianGroup creates a new custodian group. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) CreateCustodianGroup(ctx context.Context, req *transport.CreateCustodianGroupRequest) (*transport.CreateCustodianGroupResponse, error) {
	return nil, &transport.NotImplementedError{Method: "CreateCustodianGroup"}
}

// GetCustodianGroup retrieves a custodian group by ID. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) GetCustodianGroup(ctx context.Context, groupID string) (*transport.GetCustodianGroupResponse, error) {
	return nil, &transport.NotImplementedError{Method: "GetCustodianGroup"}
}

// ListCustodianGroups lists all custodian groups. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) ListCustodianGroups(ctx context.Context) (*transport.ListCustodianGroupsResponse, error) {
	return nil, &transport.NotImplementedError{Method: "ListCustodianGroups"}
}

// DeleteCustodianGroup deletes a custodian group. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) DeleteCustodianGroup(ctx context.Context, groupID string) error {
	return &transport.NotImplementedError{Method: "DeleteCustodianGroup"}
}

// AddCustodianMember adds a member to a custodian group. This method is not
// yet implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) AddCustodianMember(ctx context.Context, req *transport.AddCustodianMemberRequest) (*transport.AddCustodianMemberResponse, error) {
	return nil, &transport.NotImplementedError{Method: "AddCustodianMember"}
}

// RemoveCustodianMember removes a member from a custodian group. This method
// is not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) RemoveCustodianMember(ctx context.Context, req *transport.RemoveCustodianMemberRequest) error {
	return &transport.NotImplementedError{Method: "RemoveCustodianMember"}
}

// DistributeShares distributes Shamir shares to custodian group members. This
// method is not yet implemented for the MCP transport and requires
// corresponding MCP tool definitions.
func (t *Transport) DistributeShares(ctx context.Context, req *transport.DistributeSharesRequest) (*transport.DistributeSharesResponse, error) {
	return nil, &transport.NotImplementedError{Method: "DistributeShares"}
}
