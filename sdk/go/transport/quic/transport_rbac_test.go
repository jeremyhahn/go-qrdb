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
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var rbacBob = &transport.Identity{Subject: "bob", TenantID: "t1", Roles: []string{"reader"}}

// respondJSON returns a responder answering v with StatusOK.
func respondJSON(v any) func([]byte) (uint16, []byte) {
	return func([]byte) (uint16, []byte) {
		b, _ := json.Marshal(v)
		return wire.StatusOK, b
	}
}

func TestE2E_RBAC_ListOwnPermissions(t *testing.T) {
	srv, tr := startE2EServer(t)
	own := []transport.PermissionInfo{{Name: "kv:get", Resource: "kv", Action: "get", Scope: transport.PermissionScopeSelf}}
	srv.SetResponder("role.ListOwnPermissions", respondJSON(map[string]any{"permissions": own}))
	got, err := tr.RBACClient().ListOwnPermissions(context.Background())
	if err != nil || !slices.Equal(got, own) {
		t.Fatalf("ListOwnPermissions = %+v, %v; want %+v", got, err, own)
	}
}

func TestE2E_RBAC_ListOwnPermissionsErrors(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.ListOwnPermissions", func([]byte) (uint16, []byte) {
		return wire.StatusError, []byte("unauthenticated")
	})
	_, err := tr.RBACClient().ListOwnPermissions(context.Background())
	assertQuicRequestError(t, err)

	srv.SetResponder("role.ListOwnPermissions", func([]byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not json")
	})
	_, err = tr.RBACClient().ListOwnPermissions(context.Background())
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("ListOwnPermissions = %v, want an UnmarshalError", err)
	}
}

func TestE2E_RBAC_AuthorizeSendsTheIdentity(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Authorize", respondJSON(&transport.AuthorizeDecision{Allowed: true}))
	if err := tr.RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); err != nil {
		t.Fatalf("Authorize = %v, want nil", err)
	}
	rec := srv.LastRequest("role.Authorize")
	if rec == nil {
		t.Fatal("no recorded role.Authorize request")
	}
	var body authorizeWireRequest
	if err := json.Unmarshal(rec.Payload, &body); err != nil {
		t.Fatalf("unmarshal recorded request: %v", err)
	}
	if body.Resource != "kv" || body.Action != "get" || body.Identity == nil ||
		body.Identity.Subject != "bob" || body.Identity.TenantID != "t1" || !slices.Equal(body.Identity.Roles, rbacBob.Roles) {
		t.Fatalf("wire body = %+v", body)
	}
}

func TestE2E_RBAC_AuthorizeDeniedIsUnauthorized(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Authorize", respondJSON(&transport.AuthorizeDecision{Reason: "no grant"}))
	err := tr.RBACClient().Authorize(context.Background(), rbacBob, "kv", "put")
	var denied *transport.AuthorizationDeniedError
	if !errors.Is(err, auth.ErrUnauthorized) || !errors.As(err, &denied) || denied.Reason != "no grant" {
		t.Fatalf("Authorize = %v, want an AuthorizationDeniedError carrying the reason", err)
	}
}

func TestE2E_RBAC_AuthorizeErrors(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Authorize", func([]byte) (uint16, []byte) {
		return wire.StatusError, []byte("permissions:resolve")
	})
	assertQuicRequestError(t, tr.RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"))

	srv.SetResponder("role.Authorize", func([]byte) (uint16, []byte) {
		return wire.StatusOK, []byte("not json")
	})
	var ue *UnmarshalError
	if err := tr.RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); !errors.As(err, &ue) {
		t.Fatalf("Authorize = %v, want an UnmarshalError", err)
	}
}

func TestE2E_RBAC_ClosedTransport(t *testing.T) {
	c := newClosedTransport().RBACClient()
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
