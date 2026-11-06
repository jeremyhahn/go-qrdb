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
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var roleAnswer = transport.Role{ID: "r1", Name: "admin"}

// checkRole reports whether got decoded from roleAnswer.
func checkRole(got *transport.Role, err error) error {
	if err != nil {
		return err
	}
	if got == nil || got.ID != roleAnswer.ID || got.Name != roleAnswer.Name {
		return fmt.Errorf("role decoded as %+v, want %+v", got, roleAnswer)
	}
	return nil
}

// TestRESTRole_SendsTheDocumentedRequest pins each role and permission
// operation's verb, path, query and body on the wire, and the decoded answer.
// The CreateRole rows pin that a pre-assigned id is forwarded and an empty
// one is omitted, so the server generates it.
func TestRESTRole_SendsTheDocumentedRequest(t *testing.T) {
	update := &transport.UpdateRoleRequest{ID: "r1", Name: "admin"}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "CreateRole/PreAssignedID", verb: http.MethodPost, path: "/roles",
			wantBody: json.RawMessage(`{"id":"r1","name":"admin","permissions":null}`), answer: roleAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkRole(rt.CreateRole(ctx, &transport.CreateRoleRequest{ID: "r1", Name: "admin"}))
			}},
		{name: "CreateRole/EmptyIDOmitted", verb: http.MethodPost, path: "/roles",
			wantBody: json.RawMessage(`{"name":"admin","permissions":null}`), answer: roleAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkRole(rt.CreateRole(ctx, &transport.CreateRoleRequest{Name: "admin"}))
			}},
		{name: "GetRole", verb: http.MethodGet, path: "/roles/r1", answer: roleAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkRole(rt.GetRole(ctx, "r1"))
			}},
		{name: "UpdateRole", verb: http.MethodPut, path: "/roles/r1", wantBody: update, answer: roleAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkRole(rt.UpdateRole(ctx, update))
			}},
		{name: "DeleteRole", verb: http.MethodDelete, path: "/roles/r1", status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.DeleteRole(ctx, "r1")
			}},
		{name: "ListRoles/Paged", verb: http.MethodGet, path: "/roles/list", rawQuery: "page=2&pageSize=10&tenantID=t1",
			answer: transport.ListRolesResponse{Roles: []*transport.Role{&roleAnswer}, Total: 1},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListRoles(ctx, &transport.ListRolesRequest{TenantID: "t1", Page: 2, PageSize: 10})
				if err != nil {
					return err
				}
				if resp.Total != 1 || len(resp.Roles) != 1 {
					return fmt.Errorf("list decoded as %+v, want one role of total 1", resp)
				}
				return checkRole(resp.Roles[0], nil)
			}},
		{name: "ListRoles/NoParams", verb: http.MethodGet, path: "/roles/list", answer: transport.ListRolesResponse{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListRoles(ctx, &transport.ListRolesRequest{})
				if err == nil && len(resp.Roles) != 0 {
					return fmt.Errorf("got %d roles, want none", len(resp.Roles))
				}
				return err
			}},
		{name: "AssignRole", verb: http.MethodPost, path: "/roles/assign",
			wantBody: transport.AssignRoleRequest{UserID: 1, RoleID: "r1"}, answer: struct{}{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.AssignRole(ctx, 1, "r1")
			}},
		{name: "UnassignRole", verb: http.MethodPost, path: "/roles/unassign",
			wantBody: transport.UnassignRoleRequest{UserID: 1, RoleID: "r1"}, answer: struct{}{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.UnassignRole(ctx, 1, "r1")
			}},
		{name: "ListPermissions", verb: http.MethodGet, path: "/permissions",
			answer: transport.ListPermissionsResponse{Categories: []*transport.PermissionCategory{{Name: "kv"}}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListPermissions(ctx)
				if err != nil {
					return err
				}
				if len(resp.Categories) != 1 || resp.Categories[0].Name != "kv" {
					return fmt.Errorf("permissions decoded as %+v, want the kv category", resp.Categories)
				}
				return nil
			}},
	})
}

// TestRESTRole_SurfacesAFailedAnswer checks each role and permission
// operation turns a 5xx, and a decoding operation a malformed body, into a
// RequestError.
func TestRESTRole_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"CreateRole", http.MethodPost, "/roles", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateRole(ctx, &transport.CreateRoleRequest{Name: "admin"})
			return err
		}},
		{"GetRole", http.MethodGet, "/roles/r1", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetRole(ctx, "r1")
			return err
		}},
		{"UpdateRole", http.MethodPut, "/roles/r1", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r1"})
			return err
		}},
		{"DeleteRole", http.MethodDelete, "/roles/r1", false, func(ctx context.Context, rt *Transport) error {
			return rt.DeleteRole(ctx, "r1")
		}},
		{"ListRoles", http.MethodGet, "/roles/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListRoles(ctx, &transport.ListRolesRequest{})
			return err
		}},
		{"AssignRole", http.MethodPost, "/roles/assign", false, func(ctx context.Context, rt *Transport) error {
			return rt.AssignRole(ctx, 1, "r1")
		}},
		{"UnassignRole", http.MethodPost, "/roles/unassign", false, func(ctx context.Context, rt *Transport) error {
			return rt.UnassignRole(ctx, 1, "r1")
		}},
		{"ListPermissions", http.MethodGet, "/permissions", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListPermissions(ctx)
			return err
		}},
	})
}

// TestRESTRole_GetNotFoundIsRoleNotFoundError checks a 404 from GetRole
// becomes a RoleNotFoundError naming the role.
func TestRESTRole_GetNotFoundIsRoleNotFoundError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/roles/r1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.GetRole(context.Background(), "r1")
	var nfe *transport.RoleNotFoundError
	if !errors.As(err, &nfe) || nfe.ID != "r1" {
		t.Fatalf("GetRole = %v, want a RoleNotFoundError for r1", err)
	}
}

// TestRESTRole_Closed checks every role and permission operation refuses on
// a closed transport before it reaches the wire.
func TestRESTRole_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"CreateRole": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateRole(ctx, &transport.CreateRoleRequest{})
			return err
		},
		"GetRole": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetRole(ctx, "r1")
			return err
		},
		"UpdateRole": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateRole(ctx, &transport.UpdateRoleRequest{ID: "r1"})
			return err
		},
		"DeleteRole": func(ctx context.Context, rt *Transport) error { return rt.DeleteRole(ctx, "r1") },
		"ListRoles": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListRoles(ctx, &transport.ListRolesRequest{})
			return err
		},
		"AssignRole":   func(ctx context.Context, rt *Transport) error { return rt.AssignRole(ctx, 1, "r1") },
		"UnassignRole": func(ctx context.Context, rt *Transport) error { return rt.UnassignRole(ctx, 1, "r1") },
		"ListPermissions": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListPermissions(ctx)
			return err
		},
	})
}
