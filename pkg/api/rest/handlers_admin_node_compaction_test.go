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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCompactionAdmin is an admin double that holds the node-keyspace policy
// the way the service does: validated on set, off stored as none.
type nodeCompactionAdmin struct {
	mockAdminService
	policy *compaction.Policy
	err    error
}

func (a *nodeCompactionAdmin) NodeCompaction(context.Context) (*compaction.Policy, error) {
	return a.policy, a.err
}

func (a *nodeCompactionAdmin) SetNodeCompaction(_ context.Context, p *compaction.Policy) error {
	if a.err != nil {
		return a.err
	}
	if p != nil {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	a.policy = p.Normalized()
	return nil
}

func serveNodeCompaction(t *testing.T, admin service.AdminServicer, method, body string) (*httptest.ResponseRecorder, nodeCompactionResponse) {
	t.Helper()
	srv := NewServer(&mockKVService{}, admin, "127.0.0.1:0")
	req := httptest.NewRequest(method, "/admin/node-compaction", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	var out nodeCompactionResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec, out
}

func TestHandler_NodeCompaction_SetThenGet(t *testing.T) {
	admin := &nodeCompactionAdmin{}
	rec, out := serveNodeCompaction(t, admin, http.MethodPut, `{"compaction":{"mode":"period","retainPeriod":"720h"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%q", rec.Code, rec.Body.String())
	}
	if out.Compaction == nil || out.Compaction.Mode != "period" || out.Compaction.RetainPeriod != "720h" {
		t.Fatalf("PUT response = %+v; want the period policy", out.Compaction)
	}
	rec, out = serveNodeCompaction(t, admin, http.MethodGet, "")
	if rec.Code != http.StatusOK || out.Compaction == nil || out.Compaction.Mode != "period" {
		t.Fatalf("GET = %d %+v; want the period policy", rec.Code, out.Compaction)
	}
}

func TestHandler_NodeCompaction_AbsentPolicyTurnsItOff(t *testing.T) {
	admin := &nodeCompactionAdmin{policy: &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}}
	rec, out := serveNodeCompaction(t, admin, http.MethodPut, `{}`)
	if rec.Code != http.StatusOK || out.Compaction != nil {
		t.Fatalf("PUT {} = %d %+v; want no policy", rec.Code, out.Compaction)
	}
	if admin.policy != nil {
		t.Fatalf("policy after PUT {} = %+v; want none", admin.policy)
	}
}

func TestHandler_NodeCompaction_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		admin  *nodeCompactionAdmin
		method string
		body   string
		want   int
	}{
		{"malformed body", &nodeCompactionAdmin{}, http.MethodPut, `{`, http.StatusBadRequest},
		{"invalid policy", &nodeCompactionAdmin{}, http.MethodPut, `{"compaction":{"mode":"revision"}}`, http.StatusBadRequest},
		{"set unavailable", &nodeCompactionAdmin{err: &service.NodeCompactionUnavailableError{}}, http.MethodPut, `{}`, http.StatusUnprocessableEntity},
		{"get unavailable", &nodeCompactionAdmin{err: &service.NodeCompactionUnavailableError{}}, http.MethodGet, ``, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := serveNodeCompaction(t, tc.admin, tc.method, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%q, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}
