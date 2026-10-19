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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// joinReviewAdmin adds the join-request review and join-identity retirement
// capabilities to the mock admin service, recording what each call received.
type joinReviewAdmin struct {
	mockAdminService
	pending []*service.PendingJoinRequest
	err     error

	gotID, gotNodeID, gotReason string
}

var (
	_ service.JoinRequestReviewer = (*joinReviewAdmin)(nil)
	_ service.JoinIdentityRetirer = (*joinReviewAdmin)(nil)
)

func (a *joinReviewAdmin) ListPendingJoinRequests(context.Context) ([]*service.PendingJoinRequest, error) {
	return a.pending, a.err
}

func (a *joinReviewAdmin) ApproveJoinRequest(_ context.Context, id, nodeID string) error {
	a.gotID, a.gotNodeID = id, nodeID
	return a.err
}

func (a *joinReviewAdmin) RejectJoinRequest(_ context.Context, id, reason string) error {
	a.gotID, a.gotReason = id, reason
	return a.err
}

func (a *joinReviewAdmin) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	a.gotNodeID, a.gotReason = nodeID, reason
	return a.err
}

// callJoinReview invokes handler on a server whose admin service is admin.
func callJoinReview(admin service.AdminServicer, handler func(*Server) http.HandlerFunc, body string) *httptest.ResponseRecorder {
	srv := NewServer(&mockKVService{}, admin, "127.0.0.1:0")
	rec := httptest.NewRecorder()
	handler(srv)(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec
}

var joinReviewHandlers = map[string]func(*Server) http.HandlerFunc{
	"list":    func(s *Server) http.HandlerFunc { return s.handleListPendingJoinRequests },
	"approve": func(s *Server) http.HandlerFunc { return s.handleApproveJoinRequest },
	"reject":  func(s *Server) http.HandlerFunc { return s.handleRejectJoinRequest },
	"retire":  func(s *Server) http.HandlerFunc { return s.handleRetireJoinIdentity },
}

func TestHandler_JoinReview_UnsupportedAdminIsNotImplemented(t *testing.T) {
	for name, h := range joinReviewHandlers {
		t.Run(name, func(t *testing.T) {
			rec := callJoinReview(&mockAdminService{}, h, `{}`)
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d body=%q, want 501", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_JoinReview_ServiceErrorIsNotOK(t *testing.T) {
	for name, h := range joinReviewHandlers {
		t.Run(name, func(t *testing.T) {
			rec := callJoinReview(&joinReviewAdmin{err: errors.New("boom")}, h, `{"id":"r1"}`)
			if rec.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, want an error status", rec.Code)
			}
		})
	}
}

func TestHandler_JoinReview_MalformedBodyIsBadRequest(t *testing.T) {
	for _, name := range []string{"approve", "reject", "retire"} {
		t.Run(name, func(t *testing.T) {
			rec := callJoinReview(&joinReviewAdmin{}, joinReviewHandlers[name], `{`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%q, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_ListPendingJoinRequests_RendersEachRequest(t *testing.T) {
	admin := &joinReviewAdmin{pending: []*service.PendingJoinRequest{
		{ID: "r1", NodeURI: "spiffe://db.example.com/node/a"},
		{ID: "r2", NodeURI: "spiffe://db.example.com/node/b"},
	}}
	rec := callJoinReview(admin, joinReviewHandlers["list"], ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
	}
	var out listPendingJoinRequestsResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Requests) != 2 || out.Requests[0].ID != "r1" || out.Requests[1].NodeURI != "spiffe://db.example.com/node/b" {
		t.Fatalf("requests = %+v", out.Requests)
	}
}

func TestHandler_ListPendingJoinRequests_EmptyIsEmptyList(t *testing.T) {
	rec := callJoinReview(&joinReviewAdmin{}, joinReviewHandlers["list"], ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `[]`) {
		t.Fatalf("body = %q, want an empty requests list", rec.Body.String())
	}
}

func TestHandler_JoinReview_DecisionsReachTheService(t *testing.T) {
	for _, tc := range []struct {
		name, body                   string
		wantID, wantNode, wantReason string
	}{
		{"approve", `{"id":"r1","node_id":"n1"}`, "r1", "n1", ""},
		{"reject", `{"id":"r2","reason":"unknown host"}`, "r2", "", "unknown host"},
		{"retire", `{"node_id":"n3","reason":"decommissioned"}`, "", "n3", "decommissioned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := &joinReviewAdmin{}
			rec := callJoinReview(admin, joinReviewHandlers[tc.name], tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
			}
			if admin.gotID != tc.wantID || admin.gotNodeID != tc.wantNode || admin.gotReason != tc.wantReason {
				t.Fatalf("service got (%q,%q,%q), want (%q,%q,%q)",
					admin.gotID, admin.gotNodeID, admin.gotReason, tc.wantID, tc.wantNode, tc.wantReason)
			}
		})
	}
}
