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
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogCall is one call the recording LogService received.
type vlogCall struct {
	method string
	tenant string
	ref    service.LogRef
	req    any
}

// recordingLogService records each call and answers it with err, or with a
// fixed well-formed response.
type recordingLogService struct {
	mu    sync.Mutex
	calls []vlogCall
	err   error
}

var _ service.LogService = (*recordingLogService)(nil)

func (s *recordingLogService) record(ctx context.Context, method string, ref service.LogRef, req any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, vlogCall{method: method, tenant: tenant.FromContext(ctx), ref: ref, req: req})
	return s.err
}

func (s *recordingLogService) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *recordingLogService) lastCall(t *testing.T) vlogCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("the LogService was not called")
	}
	return s.calls[len(s.calls)-1]
}

func (s *recordingLogService) CreateLog(ctx context.Context, req *service.CreateLogRequest) (*service.CreateLogResponse, error) {
	if err := s.record(ctx, "CreateLog", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.CreateLogResponse{
		ID:  vlog.LogID{TenantID: tenant.FromContext(ctx), Database: req.Database, Log: req.Log},
		Key: &vlog.HeadKey{Version: 1, Ref: "k/1", PublicKey: []byte("pub")},
	}, nil
}

func (s *recordingLogService) ListLogs(ctx context.Context, req *service.ListLogsRequest) (*service.ListLogsResponse, error) {
	if err := s.record(ctx, "ListLogs", service.LogRef{TenantID: req.TenantID, Database: req.Database}, req); err != nil {
		return nil, err
	}
	return &service.ListLogsResponse{Logs: []vlog.LogID{{TenantID: "acme", Database: req.Database, Log: "audit"}}}, nil
}

func (s *recordingLogService) Append(ctx context.Context, req *service.AppendRequest) (*service.AppendResponse, error) {
	if err := s.record(ctx, "Append", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.AppendResponse{FirstIndex: 7, Count: len(req.Entries)}, nil
}

func (s *recordingLogService) GetEntries(ctx context.Context, req *service.GetEntriesRequest) (*service.GetEntriesResponse, error) {
	if err := s.record(ctx, "GetEntries", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.GetEntriesResponse{Entries: []vlog.Entry{{Index: req.From, Body: []byte("e")}, {Index: req.From + 1, Expired: true}}}, nil
}

func (s *recordingLogService) ProveInclusion(ctx context.Context, req *service.InclusionRequest) (*service.InclusionResponse, error) {
	if err := s.record(ctx, "ProveInclusion", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.InclusionResponse{Index: req.Index, Size: req.Size, Hashes: [][]byte{[]byte("h")}}, nil
}

func (s *recordingLogService) ProveConsistency(ctx context.Context, req *service.ConsistencyRequest) (*service.ConsistencyResponse, error) {
	if err := s.record(ctx, "ProveConsistency", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.ConsistencyResponse{Size1: req.Size1, Size2: req.Size2, Hashes: [][]byte{[]byte("c")}}, nil
}

func (s *recordingLogService) Head(ctx context.Context, req *service.HeadRequest) (*service.HeadResponse, error) {
	if err := s.record(ctx, "Head", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.HeadResponse{Head: vlog.Head{Size: 3, Root: []byte("root"), BodiesFrom: 1, Checkpoint: []byte("cp"), CheckpointSize: 2}}, nil
}

func (s *recordingLogService) SignHead(ctx context.Context, req *service.SignHeadRequest) (*service.SignHeadResponse, error) {
	if err := s.record(ctx, "SignHead", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.SignHeadResponse{
		Head:       vlog.SignedHead{Size: 3, Root: []byte("root"), KeyVersion: 1, SignatureType: vlog.SignatureEd25519, Signature: []byte("sig")},
		Checkpoint: []byte("note"),
	}, nil
}

func (s *recordingLogService) RotateKey(ctx context.Context, req *service.RotateKeyRequest) (*service.RotateKeyResponse, error) {
	if err := s.record(ctx, "RotateKey", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.RotateKeyResponse{Key: vlog.HeadKey{Version: 2, Ref: "k/2", PublicKey: []byte("pub2")}}, nil
}

func (s *recordingLogService) ExpireBodies(ctx context.Context, req *service.ExpireBodiesRequest) (*service.ExpireBodiesResponse, error) {
	if err := s.record(ctx, "ExpireBodies", req.LogRef, req); err != nil {
		return nil, err
	}
	return &service.ExpireBodiesResponse{Expired: req.Cutoff}, nil
}

// vlogRoute is one verifiable-log route with a well-formed request.
type vlogRoute struct {
	name, method, target, body, service string
}

var vlogRoutes = []vlogRoute{
	{"create", http.MethodPost, "/vlog/log", `{"log":"audit","origin":"db.example.com/audit","signing_key_type":"ed25519"}`, "CreateLog"},
	{"list", http.MethodGet, "/vlog/logs", "", "ListLogs"},
	{"append", http.MethodPost, "/vlog/append", `{"log":"audit","entries":["ZQ=="]}`, "Append"},
	{"get", http.MethodGet, "/vlog/entries?log=audit&from=2&limit=5", "", "GetEntries"},
	{"inclusion", http.MethodGet, "/vlog/proof/inclusion?log=audit&index=1&size=3", "", "ProveInclusion"},
	{"consistency", http.MethodGet, "/vlog/proof/consistency?log=audit&size1=1&size2=3", "", "ProveConsistency"},
	{"head", http.MethodGet, "/vlog/head?log=audit", "", "Head"},
	{"sign-head", http.MethodPost, "/vlog/head/sign", `{"log":"audit"}`, "SignHead"},
	{"rotate-key", http.MethodPost, "/vlog/key/rotate", `{"log":"audit","signing_key_type":"ed25519"}`, "RotateKey"},
	{"expire", http.MethodPost, "/vlog/expire", `{"log":"audit","cutoff":2}`, "ExpireBodies"},
}

// vlogRESTGrants is each built-in role's vlog grants: Admin is refused
// sign-head, which is the Operator's, and the Auditor every write.
var vlogRESTGrants = map[string]map[string]bool{
	role.RoleNameAdmin: {
		"create": true, "list": true, "append": true, "get": true,
		"inclusion": true, "consistency": true, "head": true, "expire": true,
	},
	role.RoleNameOperator: {
		"list": true, "append": true, "get": true,
		"inclusion": true, "consistency": true, "head": true, "sign-head": true,
		"rotate-key": true,
	},
	role.RoleNameUser: {
		"list": true, "append": true, "get": true,
		"inclusion": true, "consistency": true, "head": true,
	},
	role.RoleNameAuditor: {
		"list": true, "get": true,
		"inclusion": true, "consistency": true, "head": true,
	},
}

const vlogRESTTenant = "acme"

// serveVlog sends rt through the full REST handler chain, authenticated as a
// caller holding roleName in the test tenant, with the database header set.
func serveVlog(t *testing.T, roles role.RoleServicer, roleName string, rt vlogRoute, svc service.LogService) *httptest.ResponseRecorder {
	t.Helper()
	caller := &auth.Identity{
		Subject:  roleName + "-caller",
		TenantID: vlogRESTTenant,
		Roles:    []string{role.SystemRoleID(vlogRESTTenant, roleName)},
	}
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))),
		WithLogService(svc))
	req := httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body))
	req.Header.Set(HeaderDatabase, "ledger")
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// TestVlogRoutes_AuthorizeEveryBuiltInRole sends each route as each built-in
// role through the real middleware and RBAC: an admitted request reaches the
// LogService confined to the caller's tenant and the request's database, a
// refused one is 403 and never does.
func TestVlogRoutes_AuthorizeEveryBuiltInRole(t *testing.T) {
	roles := authtest.BuiltInRoles(t, vlogRESTTenant)
	for roleName, granted := range vlogRESTGrants {
		for _, rt := range vlogRoutes {
			t.Run(roleName+"/"+rt.name, func(t *testing.T) {
				svc := &recordingLogService{}
				rec := serveVlog(t, roles, roleName, rt, svc)
				if !granted[rt.name] {
					if rec.Code != http.StatusForbidden {
						t.Fatalf("status = %d body=%q, want 403", rec.Code, rec.Body.String())
					}
					if n := svc.callCount(); n != 0 {
						t.Fatalf("a refused request reached the LogService %d time(s)", n)
					}
					return
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d body=%q, want 200", rec.Code, rec.Body.String())
				}
				call := svc.lastCall(t)
				if call.method != rt.service {
					t.Fatalf("LogService method = %q, want %q", call.method, rt.service)
				}
				if call.tenant != vlogRESTTenant {
					t.Fatalf("context tenant = %q, want %q", call.tenant, vlogRESTTenant)
				}
				if call.ref.TenantID != "" || call.ref.Database != "ledger" {
					t.Fatalf("ref = %+v, want the caller's tenant (empty) in database ledger", call.ref)
				}
				if rt.name != "list" && call.ref.Log != "audit" {
					t.Fatalf("log = %q, want audit", call.ref.Log)
				}
			})
		}
	}
}

// TestVlogRoutes_TranslateRequests checks each handler hands the service the
// request's values and writes its answer.
func TestVlogRoutes_TranslateRequests(t *testing.T) {
	cases := []struct {
		route string
		check func(t *testing.T, req any, body string)
	}{
		{"create", func(t *testing.T, req any, body string) {
			r := req.(*service.CreateLogRequest)
			if r.Origin != "db.example.com/audit" || r.SigningKeyType != vlog.SignatureEd25519 {
				t.Fatalf("request = %+v", r)
			}
			if !strings.Contains(body, `"public_key":"cHVi"`) || !strings.Contains(body, `"database":"ledger"`) {
				t.Fatalf("body = %s", body)
			}
		}},
		{"rotate-key", func(t *testing.T, req any, body string) {
			if r := req.(*service.RotateKeyRequest); r.SigningKeyType != vlog.SignatureEd25519 || r.Log != "audit" {
				t.Fatalf("request = %+v", r)
			}
			if !strings.Contains(body, `"version":2`) || !strings.Contains(body, `"public_key":"cHViMg=="`) {
				t.Fatalf("body = %s", body)
			}
		}},
		{"append", func(t *testing.T, req any, body string) {
			if r := req.(*service.AppendRequest); len(r.Entries) != 1 || string(r.Entries[0]) != "e" {
				t.Fatalf("entries = %q", r.Entries)
			}
			if !strings.Contains(body, `"first_index":7`) || !strings.Contains(body, `"count":1`) {
				t.Fatalf("body = %s", body)
			}
		}},
		{"get", func(t *testing.T, req any, body string) {
			if r := req.(*service.GetEntriesRequest); r.From != 2 || r.Limit != 5 {
				t.Fatalf("request = %+v", r)
			}
			if !strings.Contains(body, `{"index":3,"expired":true}`) {
				t.Fatalf("body = %s", body)
			}
		}},
		{"inclusion", func(t *testing.T, req any, body string) {
			if r := req.(*service.InclusionRequest); r.Index != 1 || r.Size != 3 {
				t.Fatalf("request = %+v", r)
			}
			if !strings.Contains(body, `"index":1,"size":3`) {
				t.Fatalf("body = %s", body)
			}
		}},
		{"consistency", func(t *testing.T, req any, body string) {
			if r := req.(*service.ConsistencyRequest); r.Size1 != 1 || r.Size2 != 3 {
				t.Fatalf("request = %+v", r)
			}
		}},
		{"head", func(t *testing.T, _ any, body string) {
			var h vlogwire.Head
			if err := json.Unmarshal([]byte(body), &h); err != nil || h.Size != 3 || string(h.Root) != "root" || h.CheckpointSize != 2 {
				t.Fatalf("head = %+v (%v)", h, err)
			}
		}},
		{"sign-head", func(t *testing.T, _ any, body string) {
			var h vlogwire.SignHeadResult
			if err := json.Unmarshal([]byte(body), &h); err != nil || h.Head.SignatureType != vlog.SignatureEd25519 || string(h.Checkpoint) != "note" {
				t.Fatalf("signed head = %+v (%v)", h, err)
			}
		}},
		{"expire", func(t *testing.T, req any, body string) {
			if r := req.(*service.ExpireBodiesRequest); r.Cutoff != 2 {
				t.Fatalf("request = %+v", r)
			}
			if !strings.Contains(body, `"expired":2`) {
				t.Fatalf("body = %s", body)
			}
		}},
	}
	roles := authtest.BuiltInRoles(t, vlogRESTTenant)
	for _, c := range cases {
		t.Run(c.route, func(t *testing.T) {
			rt := vlogRouteNamed(t, c.route)
			roleName := role.RoleNameAdmin
			if c.route == "sign-head" || c.route == "rotate-key" {
				roleName = role.RoleNameOperator
			}
			svc := &recordingLogService{}
			rec := serveVlog(t, roles, roleName, rt, svc)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
			}
			c.check(t, svc.lastCall(t).req, rec.Body.String())
		})
	}
}

func vlogRouteNamed(t *testing.T, name string) vlogRoute {
	t.Helper()
	for _, rt := range vlogRoutes {
		if rt.name == name {
			return rt
		}
	}
	t.Fatalf("no vlog route %q", name)
	return vlogRoute{}
}

// TestVlogRoutes_RefuseBadInput checks malformed requests are refused with
// the typed error before the service is reached.
func TestVlogRoutes_RefuseBadInput(t *testing.T) {
	cases := []struct {
		name, method, target, body string
		want                       error
	}{
		{"create body", http.MethodPost, "/vlog/log", `{"log":`, &ReadBodyError{}},
		{"append body", http.MethodPost, "/vlog/append", `[]`, &ReadBodyError{}},
		{"sign-head body", http.MethodPost, "/vlog/head/sign", `x`, &ReadBodyError{}},
		{"rotate-key body", http.MethodPost, "/vlog/key/rotate", `x`, &ReadBodyError{}},
		{"expire cutoff", http.MethodPost, "/vlog/expire", `{"log":"audit","cutoff":-1}`, &ReadBodyError{}},
		{"get no log", http.MethodGet, "/vlog/entries", "", &MissingParameterError{Param: "log"}},
		{"get bad from", http.MethodGet, "/vlog/entries?log=audit&from=x", "", &InvalidParameterError{Param: "from"}},
		{"get bad limit", http.MethodGet, "/vlog/entries?log=audit&limit=-1", "", &InvalidParameterError{Param: "limit"}},
		{"inclusion no index", http.MethodGet, "/vlog/proof/inclusion?log=audit&size=3", "", &MissingParameterError{Param: "index"}},
		{"inclusion bad size", http.MethodGet, "/vlog/proof/inclusion?log=audit&index=1&size=z", "", &InvalidParameterError{Param: "size"}},
		{"consistency no log", http.MethodGet, "/vlog/proof/consistency?size1=1&size2=2", "", &MissingParameterError{Param: "log"}},
		{"consistency no size1", http.MethodGet, "/vlog/proof/consistency?log=audit&size2=2", "", &MissingParameterError{Param: "size1"}},
		{"consistency bad size2", http.MethodGet, "/vlog/proof/consistency?log=audit&size1=1&size2=q", "", &InvalidParameterError{Param: "size2"}},
		{"head no log", http.MethodGet, "/vlog/head", "", &MissingParameterError{Param: "log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &recordingLogService{}
			s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithLogService(svc))
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(c.method, c.target, strings.NewReader(c.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%q, want 400", rec.Code, rec.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if prefix := vlogErrorPrefix(c.want); !strings.HasPrefix(body.Error, prefix) {
				t.Fatalf("error = %q, want the %T refusal %q", body.Error, c.want, prefix)
			}
			if n := svc.callCount(); n != 0 {
				t.Fatalf("a refused request reached the LogService %d time(s)", n)
			}
		})
	}
}

// vlogErrorPrefix is the message of want up to its wrapped cause, the part a
// refusal of that type always begins with.
func vlogErrorPrefix(want error) string {
	const cause = "\x00"
	switch e := want.(type) {
	case *ReadBodyError:
		e.Err = errors.New(cause)
	case *InvalidParameterError:
		e.Err = errors.New(cause)
	}
	prefix, _, _ := strings.Cut(want.Error(), cause)
	return prefix
}

// TestVlogRoutes_MapServiceErrors checks a typed vlog refusal is answered
// with its class's status.
func TestVlogRoutes_MapServiceErrors(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&vlog.InvalidLogNameError{}, http.StatusBadRequest},
		{&vlog.LogNotFoundError{}, http.StatusNotFound},
		{&vlog.LogExistsError{}, http.StatusConflict},
		{&vlog.SizeOutOfRangeError{}, http.StatusRequestedRangeNotSatisfiable},
		{&vlog.ExpireNotAllowedError{}, http.StatusConflict},
		{&vlog.KeyVersionConflictError{}, http.StatusConflict},
		{&vlog.CorruptTableError{}, http.StatusInternalServerError},
		{&vlog.NotCompiledError{}, http.StatusNotImplemented},
		{&service.LogFeatureNotActiveError{}, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(errorTypeName(c.err), func(t *testing.T) {
			s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithLogService(&recordingLogService{err: c.err}))
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vlog/head?log=audit", nil))
			if rec.Code != c.want {
				t.Fatalf("status = %d body=%q, want %d", rec.Code, rec.Body.String(), c.want)
			}
		})
	}
}

func errorTypeName(err error) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
}

// TestVlogRoutes_FollowerForwardsToLeader checks a request a follower refuses
// as not the leader is relayed to the leader, body and query intact, and the
// leader's answer returned.
func TestVlogRoutes_FollowerForwardsToLeader(t *testing.T) {
	for _, name := range []string{"append", "head"} {
		t.Run(name, func(t *testing.T) {
			rt := vlogRouteNamed(t, name)
			var mu sync.Mutex
			var sawTarget, sawBody string
			leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				sawTarget, sawBody = r.URL.RequestURI(), string(body)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"relayed":true}`))
			}))
			defer leader.Close()

			reg := NewPeerAdminRegistry("63002")
			reg.Register("10.0.0.2:63001", leader.URL)
			svc := &recordingLogService{err: &service.NotLeaderError{ShardID: 9, LeaderID: 2, LeaderAddress: "10.0.0.2:63001"}}
			s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithLogService(svc), WithLeaderForwarding(reg))
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body)))

			assertStatus(t, rec, http.StatusOK)
			if !strings.Contains(rec.Body.String(), "relayed") {
				t.Fatalf("follower answered %q, not the leader's answer", rec.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if sawTarget != rt.target || sawBody != rt.body {
				t.Fatalf("leader saw %q %q, want %q %q", sawTarget, sawBody, rt.target, rt.body)
			}
		})
	}
}

// TestVlogRoutes_NotLeaderWithoutResolver checks a follower with no
// forwarding resolver answers the refusal itself.
func TestVlogRoutes_NotLeaderWithoutResolver(t *testing.T) {
	svc := &recordingLogService{err: &service.NotLeaderError{LeaderAddress: "10.0.0.2:63001"}}
	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithLogService(svc))
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vlog/append", strings.NewReader(`{"log":"audit"}`)))
	assertStatus(t, rec, http.StatusServiceUnavailable)
}

// TestVlogRoutes_NoLogService checks every route answers 501 when the node
// serves no verifiable logs.
func TestVlogRoutes_NoLogService(t *testing.T) {
	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0")
	for _, rt := range vlogRoutes {
		t.Run(rt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.target, strings.NewReader(rt.body)))
			assertStatus(t, rec, http.StatusNotImplemented)
		})
	}
}

// TestWithLogService sets and clears the server's LogService.
func TestWithLogService(t *testing.T) {
	svc := &recordingLogService{}
	if s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithLogService(svc)); s.logSvc != svc {
		t.Fatal("WithLogService did not set the LogService")
	}
	if s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithLogService(nil)); s.logSvc != nil {
		t.Fatal("WithLogService(nil) set a LogService")
	}
}
