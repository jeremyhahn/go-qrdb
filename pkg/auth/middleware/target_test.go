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

package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// targetUsers answers GetUser from a fixed map, or with err.
type targetUsers struct {
	user.UserServicer
	records map[uint64]*user.User
	err     error
}

func (u *targetUsers) GetUser(_ context.Context, id uint64) (*user.User, error) {
	if u.err != nil {
		return nil, u.err
	}
	if r, ok := u.records[id]; ok {
		return r, nil
	}
	return nil, &user.UserNotFoundError{ID: id}
}

// targetRoles answers GetRole from a fixed map, or with err.
type targetRoles struct {
	role.RoleServicer
	records map[string]*role.Role
	err     error
}

func (r *targetRoles) GetRole(_ context.Context, id string) (*role.Role, error) {
	if r.err != nil {
		return nil, r.err
	}
	if rec, ok := r.records[id]; ok {
		return rec, nil
	}
	return nil, &role.RoleNotFoundError{ID: id}
}

// recordingAuthorizer records the tenant each decision was made against.
type recordingAuthorizer struct {
	tenants []string
	err     error
}

func (a *recordingAuthorizer) Authorize(ctx context.Context, _ *auth.Identity, _, _ string) error {
	a.tenants = append(a.tenants, tenant.FromContext(ctx))
	return a.err
}

func TestResolveTarget(t *testing.T) {
	users := &targetUsers{records: map[uint64]*user.User{7: {ID: 7, TenantID: "acme"}}}
	roles := &targetRoles{records: map[string]*role.Role{"r": {ID: "r", TenantID: "globex"}}}
	storeDown := errors.New("store down")
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)

	tests := []struct {
		name    string
		mw      *AuthMiddleware
		target  Target
		want    string
		wantErr bool
	}{
		{name: "self", mw: New(nil), target: SelfTarget(), want: tenantid.System},
		{name: "empty tenant is self", mw: New(nil), target: TenantTarget(""), want: tenantid.System},
		{name: "zero user is self", mw: New(nil), target: UserTarget(0), want: tenantid.System},
		{name: "empty role is self", mw: New(nil), target: RoleTarget(""), want: tenantid.System},
		{name: "named tenant", mw: New(nil), target: TenantTarget("acme"), want: "acme"},
		{name: "user's tenant", mw: New(nil, WithUserService(users)), target: UserTarget(7), want: "acme"},
		{name: "unseen user is self", mw: New(nil, WithUserService(users)), target: UserTarget(8), want: tenantid.System},
		{name: "no user store is self", mw: New(nil), target: UserTarget(7), want: tenantid.System},
		{name: "user store failure", mw: New(nil, WithUserService(&targetUsers{err: storeDown})), target: UserTarget(7), wantErr: true},
		{name: "role's tenant", mw: New(nil, WithRoleService(roles)), target: RoleTarget("r"), want: "globex"},
		{name: "unseen role is self", mw: New(nil, WithRoleService(roles)), target: RoleTarget("x"), want: tenantid.System},
		{name: "no role store is self", mw: New(nil), target: RoleTarget("r"), want: tenantid.System},
		{name: "role store failure", mw: New(nil, WithRoleService(&targetRoles{err: storeDown})), target: RoleTarget("r"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.mw.ResolveTarget(ctx, tt.target)
			if tt.wantErr {
				var unavailable *StoreUnavailableError
				if !errors.As(err, &unavailable) || !errors.Is(err, storeDown) {
					t.Fatalf("err = %v, want *StoreUnavailableError wrapping the store failure", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ResolveTarget = (%q, %v), want %q", got, err, tt.want)
			}
		})
	}
}

// TestAuthorizeTarget_DecidesAgainstTheTarget asserts the decision is made
// against the resolved target, while the caller's context keeps its own
// tenant, and that a missing identity or an unresolvable target never reaches
// the authorizer.
func TestAuthorizeTarget_DecidesAgainstTheTarget(t *testing.T) {
	authz := &recordingAuthorizer{}
	mw := New(authz, WithUserService(&targetUsers{records: map[uint64]*user.User{7: {ID: 7, TenantID: "acme"}}}))
	id := &auth.Identity{Subject: "op", TenantID: tenantid.System}
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantid.System)

	if err := mw.AuthorizeTarget(ctx, permission.UsersUpdate, UserTarget(7)); err != nil {
		t.Fatalf("AuthorizeTarget: %v", err)
	}
	if len(authz.tenants) != 1 || authz.tenants[0] != "acme" {
		t.Fatalf("decided against %v, want [acme]", authz.tenants)
	}
	if got := tenant.FromContext(ctx); got != tenantid.System {
		t.Fatalf("caller context tenant = %q, want it untouched", got)
	}

	if err := mw.AuthorizeTarget(context.Background(), permission.UsersUpdate, UserTarget(7)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("no identity: err = %v, want ErrUnauthenticated", err)
	}
	failing := New(authz, WithUserService(&targetUsers{err: errors.New("down")}))
	if err := failing.AuthorizeTarget(ctx, permission.UsersUpdate, UserTarget(7)); err == nil {
		t.Fatal("an unresolvable target must refuse")
	}
	if len(authz.tenants) != 1 {
		t.Fatalf("authorizer consulted %d times, want only the first call", len(authz.tenants))
	}
}

// testBody stands in for a handler's request type: RESTBody decodes into it
// exactly as the handler would.
type testBody struct {
	TenantID string `json:"tenant_id"`
	UserID   uint64 `json:"user_id,string"`
}

func bodyTenant() RESTTargetFunc {
	return RESTBody(func(r *testBody) Target { return TenantTarget(r.TenantID) })
}

func bodyUser() RESTTargetFunc {
	return RESTBody(func(r *testBody) Target { return UserTarget(r.UserID) })
}

func TestRESTTargetExtractors(t *testing.T) {
	params := map[string]string{"id": "7", "bad": "x", "role": "r", "tenant": "acme"}
	param := func(name string) string { return params[name] }
	get := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/x?tenantID=acme", nil) }
	body := func(s string) *http.Request { return httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(s)) }

	tests := []struct {
		name    string
		fn      RESTTargetFunc
		req     *http.Request
		want    Target
		wantErr error
	}{
		{name: "path user", fn: RESTPathUser("id"), req: get(), want: UserTarget(7)},
		{name: "path user malformed", fn: RESTPathUser("bad"), req: get(), want: SelfTarget()},
		{name: "path role", fn: RESTPathRole("role"), req: get(), want: RoleTarget("r")},
		{name: "path tenant", fn: RESTPathTenant("tenant"), req: get(), want: TenantTarget("acme")},
		{name: "query tenant", fn: RESTQueryTenant("tenantID"), req: get(), want: TenantTarget("acme")},
		{name: "body tenant", fn: bodyTenant(), req: body(`{"tenant_id":"acme"}`), want: TenantTarget("acme")},
		{name: "body tenant absent", fn: bodyTenant(), req: body(`{}`), want: SelfTarget()},
		{name: "empty body", fn: bodyTenant(), req: body(``), want: SelfTarget()},
		{name: "no body", fn: bodyTenant(), req: get(), want: SelfTarget()},
		{name: "case-variant key, last wins", fn: bodyTenant(), req: body(`{"tenant_id":"acme","TENANT_ID":"globex"}`), want: TenantTarget("globex")},
		{name: "duplicate key, last wins", fn: bodyTenant(), req: body(`{"tenant_id":"globex","tenant_id":"acme"}`), want: TenantTarget("acme")},
		{name: "body user", fn: bodyUser(), req: body(`{"user_id":"7"}`), want: UserTarget(7)},
		{name: "case-variant user, last wins", fn: bodyUser(), req: body(`{"user_id":"7","USER_ID":"9"}`), want: UserTarget(9)},
		{name: "body not json", fn: bodyTenant(), req: body(`not json`), wantErr: &TargetBodyInvalidError{}},
		{name: "body user wrong type", fn: bodyUser(), req: body(`{"user_id":true}`), wantErr: &TargetBodyInvalidError{}},
		{name: "body past the limit", fn: bodyTenant(), req: body(`{"tenant_id":"` + strings.Repeat("x", RESTTargetBodyLimit) + `"}`), wantErr: &TargetBodyTooLargeError{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(tt.req, param)
			switch want := tt.wantErr.(type) {
			case nil:
				if err != nil || got != tt.want {
					t.Fatalf("target = (%+v, %v), want %+v", got, err, tt.want)
				}
			case *TargetBodyInvalidError:
				if !errors.As(err, &want) || want.Error() == "" {
					t.Fatalf("err = %v, want *TargetBodyInvalidError", err)
				}
			case *TargetBodyTooLargeError:
				if !errors.As(err, &want) || want.Limit != RESTTargetBodyLimit || want.Error() == "" {
					t.Fatalf("err = %v, want *TargetBodyTooLargeError at the limit", err)
				}
			}
		})
	}
}

// TestRESTBodyTarget_HandsTheWholeBodyOn asserts reading the target leaves
// the handler the body exactly as sent.
func TestRESTBodyTarget_HandsTheWholeBodyOn(t *testing.T) {
	sent := `{"tenant_id":"acme","pad":"` + strings.Repeat("x", 128<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(sent))
	if _, err := bodyTenant()(req, func(string) string { return "" }); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != sent {
		t.Fatalf("handler saw %d bytes, want the %d sent", len(got), len(sent))
	}
	if err := req.Body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// failingBody fails every read.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (failingBody) Close() error             { return nil }

func TestRESTBodyTarget_ReadFailureIsInvalid(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Body = failingBody{}
	var invalid *TargetBodyInvalidError
	if _, err := bodyTenant()(req, func(string) string { return "" }); !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *TargetBodyInvalidError", err)
	}
}

func TestTargetBodyStatus(t *testing.T) {
	if got := targetBodyStatus(&TargetBodyTooLargeError{Limit: 1}); got != http.StatusRequestEntityTooLarge {
		t.Errorf("too large = %d, want 413", got)
	}
	if got := targetBodyStatus(&TargetBodyInvalidError{}); got != http.StatusBadRequest {
		t.Errorf("invalid = %d, want 400", got)
	}
}

func TestRESTRouteRegistry_Targets(t *testing.T) {
	rr := NewRESTRouteRegistry()
	rr.Register(http.MethodPut, "/users/{id}", permission.UsersUpdate)
	rr.SetTarget(http.MethodPut, "/users/{id}", RESTPathUser("id"))
	rr.Register(http.MethodGet, "/kv", permission.KVGet)

	if got, _ := rr.targetFor(httptest.NewRequest(http.MethodPut, "/users/7", nil)); got != UserTarget(7) {
		t.Errorf("target = %+v, want the path user", got)
	}
	if got, _ := rr.targetFor(httptest.NewRequest(http.MethodGet, "/kv", nil)); got != SelfTarget() {
		t.Errorf("route without a target = %+v, want self", got)
	}
	var nilRR *RESTRouteRegistry
	if got, _ := nilRR.targetFor(httptest.NewRequest(http.MethodGet, "/kv", nil)); got != SelfTarget() {
		t.Errorf("nil registry = %+v, want self", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("SetTarget on an unregistered route must panic")
		}
	}()
	rr.SetTarget(http.MethodPost, "/nope", RESTPathUser("id"))
}

func TestRegisterGRPCTargets(t *testing.T) {
	mw := New(nil)
	fn := func(any) Target { return TenantTarget("acme") }
	if err := mw.RegisterGRPCTargets(nil); err != nil {
		t.Fatalf("empty registration: %v", err)
	}
	if got := mw.grpcTarget("/svc.S/M", nil); got != SelfTarget() {
		t.Fatalf("no registration = %+v, want self", got)
	}
	var invalid *InvalidGRPCTargetError
	if err := mw.RegisterGRPCTargets(map[string]GRPCTargetFunc{"bad": fn}); !errors.As(err, &invalid) || invalid.Error() == "" {
		t.Fatalf("malformed name: err = %v, want *InvalidGRPCTargetError", err)
	}
	if err := mw.RegisterGRPCTargets(map[string]GRPCTargetFunc{"/svc.S/M": nil}); !errors.As(err, &invalid) {
		t.Fatalf("nil extractor: err = %v, want *InvalidGRPCTargetError", err)
	}
	if err := mw.RegisterGRPCTargets(map[string]GRPCTargetFunc{"/svc.S/A": fn}); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := mw.RegisterGRPCTargets(map[string]GRPCTargetFunc{"/svc.S/B": fn}); err != nil {
		t.Fatalf("register B: %v", err)
	}
	for _, m := range []string{"/svc.S/A", "/svc.S/B"} {
		if got := mw.grpcTarget(m, nil); got != TenantTarget("acme") {
			t.Errorf("%s = %+v, want both registrations kept", m, got)
		}
	}
	if got := mw.grpcTarget("/svc.S/Other", nil); got != SelfTarget() {
		t.Errorf("unregistered method = %+v, want self", got)
	}
}

func TestExactNameRegistries_Targets(t *testing.T) {
	quic := NewQUICMethodRegistry()
	quic.Register("user.Update", permission.UsersUpdate)
	quic.SetTarget("user.Update", func([]byte) (Target, error) { return UserTarget(7), nil })
	if got, _ := quic.target("user.Update", nil); got != UserTarget(7) {
		t.Errorf("quic target = %+v", got)
	}
	if got, _ := quic.target("kv.Put", nil); got != SelfTarget() {
		t.Errorf("quic method without target = %+v", got)
	}
	var nilQUIC *QUICMethodRegistry
	if got, _ := nilQUIC.target("user.Update", nil); got != SelfTarget() {
		t.Errorf("nil quic registry = %+v", got)
	}

	mcp := NewMCPToolRegistry()
	mcp.Register("qrdb_user_update", permission.UsersUpdate)
	mcp.SetTarget("qrdb_user_update", func(map[string]any) Target { return UserTarget(7) })
	if got := mcp.target("qrdb_user_update", nil); got != UserTarget(7) {
		t.Errorf("mcp target = %+v", got)
	}
	if got := mcp.target("qrdb_kv_put", nil); got != SelfTarget() {
		t.Errorf("mcp tool without target = %+v", got)
	}
	var nilMCP *MCPToolRegistry
	if got := nilMCP.target("qrdb_user_update", nil); got != SelfTarget() {
		t.Errorf("nil mcp registry = %+v", got)
	}

	for name, set := range map[string]func(){
		"quic": func() { quic.SetTarget("unregistered", func([]byte) (Target, error) { return SelfTarget(), nil }) },
		"mcp":  func() { mcp.SetTarget("unregistered", func(map[string]any) Target { return SelfTarget() }) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("SetTarget on an unregistered name must panic")
				}
			}()
			set()
		})
	}
}

// TestAuthorizeQUIC_TargetErrorRefuses asserts a payload the method's
// extractor cannot read is refused with that error, before any decision.
func TestAuthorizeQUIC_TargetErrorRefuses(t *testing.T) {
	authz := &recordingAuthorizer{}
	mw := New(authz)
	methods := NewQUICMethodRegistry()
	methods.Register("user.Update", permission.UsersUpdate)
	methods.SetTarget("user.Update", func([]byte) (Target, error) {
		return SelfTarget(), &TargetBodyInvalidError{}
	})
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "op", TenantID: tenantid.System})
	var invalid *TargetBodyInvalidError
	if err := mw.AuthorizeQUIC(ctx, methods, "user.Update", []byte("x")); !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *TargetBodyInvalidError", err)
	}
	if len(authz.tenants) != 0 {
		t.Fatalf("authorizer consulted %d times, want none", len(authz.tenants))
	}
}
