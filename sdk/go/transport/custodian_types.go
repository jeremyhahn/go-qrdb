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

import "time"

// CustodianGroupInfo contains information about a custodian group.
type CustodianGroupInfo struct {
	ID        string                `json:"id"`
	TenantID  string                `json:"tenant_id,omitempty"`
	Name      string                `json:"name"`
	Purpose   string                `json:"purpose"`
	Threshold int                   `json:"threshold"`
	Total     int                   `json:"total"`
	Members   []CustodianMemberInfo `json:"members"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// CustodianMemberInfo contains information about a custodian group member.
type CustodianMemberInfo struct {
	ShareIndex int        `json:"share_index"`
	UserID     string     `json:"user_id"`
	Username   string     `json:"username"`
	AssignedAt time.Time  `json:"assigned_at"`
	ReceivedAt *time.Time `json:"received_at,omitempty"`
	Method     string     `json:"method"`
}

// CreateCustodianGroupRequest contains parameters for creating a custodian group.
type CreateCustodianGroupRequest struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Name      string `json:"name"`
	Purpose   string `json:"purpose"`
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
}

// CreateCustodianGroupResponse contains the created custodian group.
type CreateCustodianGroupResponse struct {
	Group CustodianGroupInfo `json:"group"`
}

// GetCustodianGroupResponse contains a custodian group.
type GetCustodianGroupResponse struct {
	Group CustodianGroupInfo `json:"group"`
}

// ListCustodianGroupsResponse contains a list of custodian groups.
type ListCustodianGroupsResponse struct {
	Groups []CustodianGroupInfo `json:"groups"`
}

// AddCustodianMemberRequest contains parameters for adding a member to a group.
type AddCustodianMemberRequest struct {
	GroupID  string `json:"group_id"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Method   string `json:"method"`
}

// AddCustodianMemberResponse contains the added member.
type AddCustodianMemberResponse struct {
	Member CustodianMemberInfo `json:"member"`
}

// RemoveCustodianMemberRequest contains parameters for removing a member.
type RemoveCustodianMemberRequest struct {
	GroupID string `json:"group_id"`
	UserID  string `json:"user_id"`
}

// DistributeSharesRequest contains parameters for distributing shares.
type DistributeSharesRequest struct {
	GroupID string `json:"group_id"`
}

// DistributeSharesResponse contains the distribution result.
type DistributeSharesResponse struct {
	Distributed int `json:"distributed"`
}
