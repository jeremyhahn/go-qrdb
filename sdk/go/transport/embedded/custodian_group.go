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

// CreateCustodianGroup creates a new custodian group.
func (t *Transport) CreateCustodianGroup(ctx context.Context, req *transport.CreateCustodianGroupRequest) (*transport.CreateCustodianGroupResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return nil, &NilServiceError{Service: "CustodianGroupServicer"}
	}
	group, err := t.custodianGroupSvc.CreateGroup(
		ctx, req.ID, req.TenantID, req.Name, req.Purpose, req.Threshold, req.Total,
	)
	if err != nil {
		return nil, err
	}
	return &transport.CreateCustodianGroupResponse{Group: *group}, nil
}

// GetCustodianGroup retrieves a custodian group by ID.
func (t *Transport) GetCustodianGroup(ctx context.Context, groupID string) (*transport.GetCustodianGroupResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return nil, &NilServiceError{Service: "CustodianGroupServicer"}
	}
	group, err := t.custodianGroupSvc.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return &transport.GetCustodianGroupResponse{Group: *group}, nil
}

// ListCustodianGroups lists all custodian groups.
func (t *Transport) ListCustodianGroups(ctx context.Context) (*transport.ListCustodianGroupsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return nil, &NilServiceError{Service: "CustodianGroupServicer"}
	}
	groups, err := t.custodianGroupSvc.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.ListCustodianGroupsResponse{Groups: groups}, nil
}

// DeleteCustodianGroup deletes a custodian group.
func (t *Transport) DeleteCustodianGroup(ctx context.Context, groupID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return &NilServiceError{Service: "CustodianGroupServicer"}
	}
	return t.custodianGroupSvc.DeleteGroup(ctx, groupID)
}

// AddCustodianMember adds a member to a custodian group.
func (t *Transport) AddCustodianMember(ctx context.Context, req *transport.AddCustodianMemberRequest) (*transport.AddCustodianMemberResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return nil, &NilServiceError{Service: "CustodianGroupServicer"}
	}
	member, err := t.custodianGroupSvc.AddMember(ctx, req.GroupID, req.UserID, req.Username, req.Method)
	if err != nil {
		return nil, err
	}
	return &transport.AddCustodianMemberResponse{Member: *member}, nil
}

// RemoveCustodianMember removes a member from a custodian group.
func (t *Transport) RemoveCustodianMember(ctx context.Context, req *transport.RemoveCustodianMemberRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return &NilServiceError{Service: "CustodianGroupServicer"}
	}
	return t.custodianGroupSvc.RemoveMember(ctx, req.GroupID, req.UserID)
}

// DistributeShares distributes Shamir shares to custodian group members.
func (t *Transport) DistributeShares(ctx context.Context, req *transport.DistributeSharesRequest) (*transport.DistributeSharesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.custodianGroupSvc == nil {
		return nil, &NilServiceError{Service: "CustodianGroupServicer"}
	}
	count, err := t.custodianGroupSvc.DistributeShares(ctx, req.GroupID)
	if err != nil {
		return nil, err
	}
	return &transport.DistributeSharesResponse{Distributed: count}, nil
}

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// WithCustodianGroupService injects a CustodianGroupServicer into the
// transport.
func WithCustodianGroupService(svc ceremony.CustodianGroupServicer) Option {
	return func(t *Transport) {
		t.custodianGroupSvc = svc
	}
}
