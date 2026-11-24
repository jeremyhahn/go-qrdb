// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/group/groupauthz"
)

// createGroupWireRequest is the JSON wire payload for group.Create.
type createGroupWireRequest struct {
	TenantID    string   `json:"tenantID,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RoleIDs     []string `json:"roleIDs,omitempty"`
}

// groupIDWireRequest carries a group ID (group.Get, group.Delete).
type groupIDWireRequest struct {
	ID string `json:"id"`
}

// getGroupByNameWireRequest is the payload for group.GetByName.
type getGroupByNameWireRequest struct {
	TenantID string `json:"tenantID,omitempty"`
	Name     string `json:"name"`
}

// updateGroupWireRequest is the payload for group.Update: an empty name, an
// absent description or absent roleIDs keep the current value.
type updateGroupWireRequest struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	RoleIDs     *[]string `json:"roleIDs,omitempty"`
}

// listGroupsWireRequest is the payload for group.List.
type listGroupsWireRequest struct {
	TenantID string `json:"tenantID,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// groupMemberWireRequest is the payload for group.AddMember, group.RemoveMember
// and (groupID only) group.ListMembers.
type groupMemberWireRequest struct {
	UserID  uint64 `json:"userID,omitempty,string"`
	GroupID string `json:"groupID"`
}

type groupWireResponse struct {
	ID          string   `json:"id"`
	TenantID    string   `json:"tenantID"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RoleIDs     []string `json:"roleIDs"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type listGroupsWireResponse struct {
	Groups  []*groupWireResponse `json:"groups"`
	Total   int                  `json:"total"`
	HasMore bool                 `json:"hasMore"`
}

type groupMemberWireResponse struct {
	UserID   uint64 `json:"userID,string"`
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
}

type listGroupMembersWireResponse struct {
	Members []groupMemberWireResponse `json:"members"`
}

func (s *Server) groups() (*groupauthz.Service, error) {
	svc := groupauthz.New(s.groupSvc, s.userSvc, s.roleSvc, s.authMW)
	if svc == nil {
		return nil, &ServiceNotConfiguredError{Service: "group"}
	}
	return svc, nil
}

func decodeGroupPayload[T any](payload []byte) (*T, error) {
	var req T
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	return &req, nil
}

func marshalGroupWire(v any) ([]byte, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

func (s *Server) handleCreateGroup(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[createGroupWireRequest](payload)
	if err != nil {
		return nil, err
	}
	g, err := svc.Create(ctx, &group.CreateGroupRequest{TenantID: req.TenantID, Name: req.Name, Description: req.Description, RoleIDs: req.RoleIDs})
	if err != nil {
		return nil, err
	}
	return marshalGroupWire(groupToWire(g))
}

func (s *Server) handleGetGroup(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[groupIDWireRequest](payload)
	if err != nil {
		return nil, err
	}
	g, err := svc.Get(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return marshalGroupWire(groupToWire(g))
}

func (s *Server) handleGetGroupByName(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[getGroupByNameWireRequest](payload)
	if err != nil {
		return nil, err
	}
	g, err := svc.GetByName(ctx, req.TenantID, req.Name)
	if err != nil {
		return nil, err
	}
	return marshalGroupWire(groupToWire(g))
}

func (s *Server) handleUpdateGroup(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[updateGroupWireRequest](payload)
	if err != nil {
		return nil, err
	}
	up := &group.UpdateGroupRequest{ID: req.ID, Name: req.Name, Description: req.Description}
	if req.RoleIDs != nil {
		up.RoleIDs = append([]string{}, (*req.RoleIDs)...)
	}
	g, err := svc.Update(ctx, up)
	if err != nil {
		return nil, err
	}
	return marshalGroupWire(groupToWire(g))
}

func (s *Server) handleDeleteGroup(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[groupIDWireRequest](payload)
	if err != nil {
		return nil, err
	}
	if err := svc.Delete(ctx, req.ID); err != nil {
		return nil, err
	}
	return marshalGroupWire(statusWireResponse{Status: "ok"})
}

func (s *Server) handleListGroups(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[listGroupsWireRequest](payload)
	if err != nil {
		return nil, err
	}
	resp, err := svc.List(ctx, &group.ListGroupsRequest{TenantID: req.TenantID, Page: req.Page, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	out := make([]*groupWireResponse, len(resp.Groups))
	for i, g := range resp.Groups {
		out[i] = groupToWire(g)
	}
	return marshalGroupWire(listGroupsWireResponse{Groups: out, Total: resp.Total, HasMore: resp.HasMore})
}

func (s *Server) handleAddGroupMember(ctx context.Context, payload []byte) ([]byte, error) {
	return s.groupMember(ctx, payload, (*groupauthz.Service).AddMember)
}

func (s *Server) handleRemoveGroupMember(ctx context.Context, payload []byte) ([]byte, error) {
	return s.groupMember(ctx, payload, (*groupauthz.Service).RemoveMember)
}

func (s *Server) groupMember(ctx context.Context, payload []byte, op func(*groupauthz.Service, context.Context, uint64, string) error) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[groupMemberWireRequest](payload)
	if err != nil {
		return nil, err
	}
	if err := op(svc, ctx, req.UserID, req.GroupID); err != nil {
		return nil, err
	}
	return marshalGroupWire(statusWireResponse{Status: "ok"})
}

func (s *Server) handleListGroupMembers(ctx context.Context, payload []byte) ([]byte, error) {
	svc, err := s.groups()
	if err != nil {
		return nil, err
	}
	req, err := decodeGroupPayload[groupMemberWireRequest](payload)
	if err != nil {
		return nil, err
	}
	members, err := svc.ListMembers(ctx, req.GroupID)
	if err != nil {
		return nil, err
	}
	out := make([]groupMemberWireResponse, len(members))
	for i, m := range members {
		out[i] = groupMemberWireResponse{UserID: m.UserID, Username: m.Username, TenantID: m.TenantID}
	}
	return marshalGroupWire(listGroupMembersWireResponse{Members: out})
}

func groupToWire(g *group.Group) *groupWireResponse {
	roles := g.RoleIDs
	if roles == nil {
		roles = []string{}
	}
	return &groupWireResponse{
		ID: g.ID, TenantID: g.TenantID, Name: g.Name, Description: g.Description, RoleIDs: roles,
		CreatedAt: g.CreatedAt.Format(time.RFC3339), UpdatedAt: g.UpdatedAt.Format(time.RFC3339),
	}
}
