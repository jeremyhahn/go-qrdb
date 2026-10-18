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

package transport

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// fakeRBACEndpoints answers like a server: it records what it was asked and
// returns the configured answers.
type fakeRBACEndpoints struct {
	decision  *AuthorizeDecision
	resolved  []string
	own       []PermissionInfo
	err       error
	authorize *AuthorizeRequest
	roleIDs   []string
}

var _ RBACEndpoints = (*fakeRBACEndpoints)(nil)

func (f *fakeRBACEndpoints) ResolvePermissions(_ context.Context, req *ResolvePermissionsRequest) (*ResolvePermissionsResponse, error) {
	f.roleIDs = req.RoleIDs
	if f.err != nil {
		return nil, f.err
	}
	return &ResolvePermissionsResponse{Permissions: f.resolved}, nil
}

func (f *fakeRBACEndpoints) ListOwnPermissions(context.Context) ([]PermissionInfo, error) {
	return f.own, f.err
}

func (f *fakeRBACEndpoints) EvaluateAuthorization(_ context.Context, req *AuthorizeRequest) (*AuthorizeDecision, error) {
	f.authorize = req
	if f.err != nil {
		return nil, f.err
	}
	return f.decision, nil
}

var alice = &Identity{Subject: "alice", TenantID: "t1", Roles: []string{"reader"}}

func TestRemoteRBACClient_AuthorizeAllowedIsNil(t *testing.T) {
	f := &fakeRBACEndpoints{decision: &AuthorizeDecision{Allowed: true}}
	if err := NewRemoteRBACClient(f).Authorize(context.Background(), alice, "kv", "get"); err != nil {
		t.Fatalf("Authorize = %v, want nil", err)
	}
	want := AuthorizeRequest{Identity: alice, Resource: "kv", Action: "get"}
	if f.authorize == nil || *f.authorize != want {
		t.Fatalf("endpoint asked %+v, want %+v", f.authorize, want)
	}
}

func TestRemoteRBACClient_AuthorizeDeniedIsUnauthorized(t *testing.T) {
	f := &fakeRBACEndpoints{decision: &AuthorizeDecision{Reason: "no grant"}}
	err := NewRemoteRBACClient(f).Authorize(context.Background(), alice, "kv", "put")
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authorize = %v, want errors.Is auth.ErrUnauthorized", err)
	}
	var denied *AuthorizationDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Authorize = %T, want *AuthorizationDeniedError", err)
	}
	if *denied != (AuthorizationDeniedError{Resource: "kv", Action: "put", Reason: "no grant"}) {
		t.Fatalf("denial = %+v", denied)
	}
	if denied.Error() != "transport: authorization denied for kv:put: no grant" {
		t.Fatalf("Error() = %q", denied.Error())
	}
	if msg := (&AuthorizationDeniedError{Resource: "kv", Action: "put"}).Error(); msg != "transport: authorization denied for kv:put" {
		t.Fatalf("Error() without reason = %q", msg)
	}
}

func TestRemoteRBACClient_AuthorizeEndpointErrorPropagates(t *testing.T) {
	f := &fakeRBACEndpoints{err: auth.ErrUnauthenticated}
	if err := NewRemoteRBACClient(f).Authorize(context.Background(), alice, "kv", "get"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authorize = %v, want the endpoint's error", err)
	}
}

func TestRemoteRBACClient_InvalidIdentityNeverReachesTheServer(t *testing.T) {
	for _, id := range []*Identity{nil, {TenantID: "t1"}, {Subject: "alice"}} {
		f := &fakeRBACEndpoints{decision: &AuthorizeDecision{Allowed: true}}
		c := NewRemoteRBACClient(f)
		var invalid *InvalidIdentityError
		if err := c.Authorize(context.Background(), id, "kv", "get"); !errors.As(err, &invalid) {
			t.Fatalf("Authorize(%+v) = %v, want *InvalidIdentityError", id, err)
		}
		if _, err := c.ListPermissions(context.Background(), id); !errors.As(err, &invalid) {
			t.Fatalf("ListPermissions(%+v) = %v, want *InvalidIdentityError", id, err)
		}
		if f.authorize != nil || f.roleIDs != nil {
			t.Fatalf("an invalid identity %+v reached the server", id)
		}
	}
}

func TestRemoteRBACClient_ListPermissionsExpandsTheResolvedRoles(t *testing.T) {
	f := &fakeRBACEndpoints{resolved: []string{string(permission.ObjStoreAll), string(permission.PermissionsResolve)}}
	got, err := NewRemoteRBACClient(f).ListPermissions(context.Background(), alice)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if !slices.Equal(f.roleIDs, alice.Roles) {
		t.Fatalf("resolved roles %v, want %v", f.roleIDs, alice.Roles)
	}
	want := permission.ExpandAll([]permission.Permission{permission.ObjStoreAll, permission.PermissionsResolve})
	if len(got) != len(want) {
		t.Fatalf("got %d permissions, want %d", len(got), len(want))
	}
	for i, p := range want {
		info, err := PermissionInfoFromDomain(p)
		if err != nil {
			t.Fatalf("PermissionInfoFromDomain(%s): %v", p, err)
		}
		if got[i] != info {
			t.Fatalf("permission %d = %+v, want %+v", i, got[i], info)
		}
	}
}

func TestRemoteRBACClient_ListPermissionsErrors(t *testing.T) {
	f := &fakeRBACEndpoints{err: auth.ErrUnauthorized}
	if _, err := NewRemoteRBACClient(f).ListPermissions(context.Background(), alice); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("guarded resolve = %v, want the endpoint's error", err)
	}
	f = &fakeRBACEndpoints{resolved: []string{"nosuch:verb"}}
	if _, err := NewRemoteRBACClient(f).ListPermissions(context.Background(), alice); err == nil {
		t.Fatal("an unregistered permission was projected without its scope")
	}
}

func TestRemoteRBACClient_ListOwnPermissionsIsTheServersAnswer(t *testing.T) {
	own := []PermissionInfo{{Name: "kv:get", Resource: "kv", Action: "get", Scope: PermissionScopeSelf}}
	got, err := NewRemoteRBACClient(&fakeRBACEndpoints{own: own}).ListOwnPermissions(context.Background())
	if err != nil || !slices.Equal(got, own) {
		t.Fatalf("ListOwnPermissions = %+v, %v; want %+v", got, err, own)
	}
	if _, err := NewRemoteRBACClient(&fakeRBACEndpoints{err: auth.ErrUnauthenticated}).ListOwnPermissions(context.Background()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("ListOwnPermissions = %v, want the endpoint's error", err)
	}
}
