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
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var rbacBob = &transport.Identity{Subject: "bob", TenantID: "t1", Roles: []string{"reader"}}

func TestRESTRBAC_ListOwnPermissions(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	own := []transport.PermissionInfo{{Name: "kv:get", Resource: "kv", Action: "get", Scope: transport.PermissionScopeSelf}}
	rs.handle(exactPath(http.MethodGet, "/permissions/own"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"permissions": own})
	})
	got, err := rt.RBACClient().ListOwnPermissions(context.Background())
	if err != nil || !slices.Equal(got, own) {
		t.Fatalf("ListOwnPermissions = %+v, %v; want %+v", got, err, own)
	}
}

func TestRESTRBAC_ListOwnPermissionsServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/permissions/own"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	})
	if _, err := rt.RBACClient().ListOwnPermissions(context.Background()); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestRESTRBAC_AuthorizeSendsTheIdentity(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var body authorizeWireRequest
	rs.handle(exactPath(http.MethodPost, "/permissions/authorize"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, &transport.AuthorizeDecision{Allowed: true})
	})
	if err := rt.RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); err != nil {
		t.Fatalf("Authorize = %v, want nil", err)
	}
	if body.Resource != "kv" || body.Action != "get" || body.Identity == nil ||
		body.Identity.Subject != "bob" || body.Identity.TenantID != "t1" || !slices.Equal(body.Identity.Roles, rbacBob.Roles) {
		t.Fatalf("wire body = %+v", body)
	}
}

func TestRESTRBAC_AuthorizeDeniedIsUnauthorized(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/permissions/authorize"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.AuthorizeDecision{Reason: "no grant"})
	})
	err := rt.RBACClient().Authorize(context.Background(), rbacBob, "kv", "put")
	var denied *transport.AuthorizationDeniedError
	if !errors.Is(err, auth.ErrUnauthorized) || !errors.As(err, &denied) || denied.Reason != "no grant" {
		t.Fatalf("Authorize = %v, want an AuthorizationDeniedError carrying the reason", err)
	}
}

func TestRESTRBAC_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	c := rt.RBACClient()
	var ce *transport.ClosedError
	if _, err := c.ListOwnPermissions(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("ListOwnPermissions = %T, want ClosedError", err)
	}
	if err := c.Authorize(context.Background(), rbacBob, "kv", "get"); !errors.As(err, &ce) {
		t.Fatalf("Authorize = %T, want ClosedError", err)
	}
	if _, err := c.ListPermissions(context.Background(), rbacBob); !errors.As(err, &ce) {
		t.Fatalf("ListPermissions = %T, want ClosedError", err)
	}
}
