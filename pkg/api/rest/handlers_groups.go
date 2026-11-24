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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/group/groupauthz"
)

type createGroupRequest struct {
	TenantID    string   `json:"tenantID,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RoleIDs     []string `json:"roleIDs,omitempty"`
}

// updateGroupRequest: an empty name keeps the name, an absent description
// keeps it, and an absent roleIDs keeps the role set (an empty list clears it).
type updateGroupRequest struct {
	Name        string    `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	RoleIDs     *[]string `json:"roleIDs,omitempty"`
}

type groupMemberRequest struct {
	UserID  uint64 `json:"userID,string"`
	GroupID string `json:"groupID"`
}

type groupResponse struct {
	ID          string   `json:"id"`
	TenantID    string   `json:"tenantID"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	RoleIDs     []string `json:"roleIDs"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type listGroupsResponse struct {
	Groups  []*groupResponse `json:"groups"`
	Total   int              `json:"total"`
	HasMore bool             `json:"hasMore"`
}

type groupMemberResponse struct {
	UserID   uint64 `json:"userID,string"`
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
}

type listGroupMembersResponse struct {
	Members []groupMemberResponse `json:"members"`
}

// groups returns the IAM group service or writes 501.
func (s *Server) groups(w http.ResponseWriter) *groupauthz.Service {
	svc := groupauthz.New(s.groupSvc, s.userSvc, s.roleSvc, s.authMW)
	if svc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "group"})
	}
	return svc
}

// handleCreateGroup handles POST /groups.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	svc := s.groups(w)
	if svc == nil {
		return
	}
	var req createGroupRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	g, err := svc.Create(ctx, &group.CreateGroupRequest{
		TenantID: req.TenantID, Name: req.Name, Description: req.Description, RoleIDs: req.RoleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(groupToResponse(g))
}

// handleListGroups handles GET /groups/list?tenantID=&page=&pageSize=.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	svc := s.groups(w)
	if svc == nil {
		return
	}
	q := r.URL.Query()
	req := &group.ListGroupsRequest{TenantID: q.Get("tenantID")}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"page", &req.Page}, {"pageSize", &req.PageSize}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: p.name, Err: err})
				return
			}
			*p.dst = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	resp, err := svc.List(ctx, req)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	out := make([]*groupResponse, len(resp.Groups))
	for i, g := range resp.Groups {
		out[i] = groupToResponse(g)
	}
	writeJSON(w, listGroupsResponse{Groups: out, Total: resp.Total, HasMore: resp.HasMore})
}

// handleGetGroupByName handles GET /groups/by-name?tenantID=&name=.
func (s *Server) handleGetGroupByName(w http.ResponseWriter, r *http.Request) {
	svc := s.groups(w)
	if svc == nil {
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	g, err := svc.GetByName(ctx, q.Get("tenantID"), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, groupToResponse(g))
}

// handleGroup handles GET, PUT and DELETE /groups/{id}.
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	svc := s.groups(w)
	if svc == nil {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/groups/")
	if id == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		g, err := svc.Get(ctx, id)
		if err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
		writeJSON(w, groupToResponse(g))
	case http.MethodPut:
		var req updateGroupRequest
		if !decodeIAMRequest(w, r, &req) {
			return
		}
		up := &group.UpdateGroupRequest{ID: id, Name: req.Name, Description: req.Description}
		if req.RoleIDs != nil {
			up.RoleIDs = append([]string{}, (*req.RoleIDs)...)
		}
		g, err := svc.Update(ctx, up)
		if err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
		writeJSON(w, groupToResponse(g))
	case http.MethodDelete:
		if err := svc.Delete(ctx, id); err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAddGroupMember handles POST /groups/members/add.
func (s *Server) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	s.handleGroupMember(w, r, (*groupauthz.Service).AddMember)
}

// handleRemoveGroupMember handles POST /groups/members/remove.
func (s *Server) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	s.handleGroupMember(w, r, (*groupauthz.Service).RemoveMember)
}

func (s *Server) handleGroupMember(w http.ResponseWriter, r *http.Request, op func(*groupauthz.Service, context.Context, uint64, string) error) {
	svc := s.groups(w)
	if svc == nil {
		return
	}
	var req groupMemberRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}
	if req.GroupID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "groupID"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	if err := op(svc, ctx, req.UserID, req.GroupID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListGroupMembers handles GET /groups/members?groupID=.
func (s *Server) handleListGroupMembers(w http.ResponseWriter, r *http.Request) {
	svc := s.groups(w)
	if svc == nil {
		return
	}
	gid := r.URL.Query().Get("groupID")
	if gid == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "groupID"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	members, err := svc.ListMembers(ctx, gid)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	out := make([]groupMemberResponse, len(members))
	for i, m := range members {
		out[i] = groupMemberResponse{UserID: m.UserID, Username: m.Username, TenantID: m.TenantID}
	}
	writeJSON(w, listGroupMembersResponse{Members: out})
}

func groupToResponse(g *group.Group) *groupResponse {
	roles := g.RoleIDs
	if roles == nil {
		roles = []string{}
	}
	return &groupResponse{
		ID: g.ID, TenantID: g.TenantID, Name: g.Name, Description: g.Description, RoleIDs: roles,
		CreatedAt: g.CreatedAt.Format(time.RFC3339), UpdatedAt: g.UpdatedAt.Format(time.RFC3339),
	}
}
