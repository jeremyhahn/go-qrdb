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

// nullBody is the wire body of a POST whose payload is nil.
var nullBody = json.RawMessage("null")

var userAnswer = transport.User{ID: 1, Username: "alice"}

// checkUser reports whether got decoded from userAnswer.
func checkUser(got *transport.User, err error) error {
	if err != nil {
		return err
	}
	if got == nil || got.ID != userAnswer.ID || got.Username != userAnswer.Username {
		return fmt.Errorf("user decoded as %+v, want %+v", got, userAnswer)
	}
	return nil
}

// TestRESTUser_SendsTheDocumentedRequest pins each user operation's verb,
// path, query and body on the wire, and the decoded answer.
func TestRESTUser_SendsTheDocumentedRequest(t *testing.T) {
	create := &transport.CreateUserRequest{WithoutEmailLoginKey: true, Username: "alice"}
	update := &transport.UpdateUserRequest{ID: 1}

	cases := []wireCase{
		{name: "CreateUser", verb: http.MethodPost, path: "/users", wantBody: create, answer: userAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.CreateUser(ctx, create))
			}},
		{name: "GetUser", verb: http.MethodGet, path: "/users/1", answer: userAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.GetUser(ctx, 1))
			}},
		{name: "GetUserByUsername/Tenant", verb: http.MethodGet, path: "/users/by-username/alice",
			rawQuery: "tenantID=t1", answer: userAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.GetUserByUsername(ctx, "t1", "alice"))
			}},
		{name: "GetUserByUsername/NoTenant", verb: http.MethodGet, path: "/users/by-username/alice", answer: userAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.GetUserByUsername(ctx, "", "alice"))
			}},
		{name: "UpdateUser", verb: http.MethodPut, path: "/users/1", wantBody: update, answer: userAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return checkUser(rt.UpdateUser(ctx, update))
			}},
		{name: "DeleteUser", verb: http.MethodDelete, path: "/users/1", status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.DeleteUser(ctx, 1)
			}},
		{name: "ListUsers/Paged", verb: http.MethodGet, path: "/users/list", rawQuery: "page=2&pageSize=10&tenantID=t1",
			answer: transport.ListUsersResponse{Users: []*transport.User{&userAnswer}, Total: 1},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListUsers(ctx, &transport.ListUsersRequest{TenantID: "t1", Page: 2, PageSize: 10})
				if err != nil {
					return err
				}
				if resp.Total != 1 || len(resp.Users) != 1 {
					return fmt.Errorf("list decoded as %+v, want one user of total 1", resp)
				}
				return checkUser(resp.Users[0], nil)
			}},
		{name: "ListUsers/NoParams", verb: http.MethodGet, path: "/users/list", answer: transport.ListUsersResponse{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListUsers(ctx, &transport.ListUsersRequest{})
				if err == nil && len(resp.Users) != 0 {
					return fmt.Errorf("got %d users, want none", len(resp.Users))
				}
				return err
			}},
	}
	for name, call := range userStateChanges {
		cases = append(cases, wireCase{name: name, verb: http.MethodPost, path: "/users/1/" + call.verb,
			wantBody: nullBody, answer: struct{}{},
			callCheck: func(ctx context.Context, rt *Transport) error { return call.do(rt, ctx, 1) }})
	}
	assertSendsTheDocumentedRequest(t, cases)
}

// userStateChanges are the body-less POSTs that move a user between states,
// keyed by method name with the path segment each one posts to.
var userStateChanges = map[string]struct {
	verb string
	do   func(rt *Transport, ctx context.Context, id uint64) error
}{
	"EnableUser":    {"enable", (*Transport).EnableUser},
	"DisableUser":   {"disable", (*Transport).DisableUser},
	"SuspendUser":   {"suspend", (*Transport).SuspendUser},
	"UnsuspendUser": {"unsuspend", (*Transport).UnsuspendUser},
}

// TestRESTUser_SurfacesAFailedAnswer checks each user operation turns a 5xx,
// and a decoding operation a malformed body, into a RequestError.
func TestRESTUser_SurfacesAFailedAnswer(t *testing.T) {
	cases := []failedAnswerCase{
		{"CreateUser", http.MethodPost, "/users", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true})
			return err
		}},
		{"GetUser", http.MethodGet, "/users/1", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUser(ctx, 1)
			return err
		}},
		{"GetUserByUsername", http.MethodGet, "/users/by-username/alice", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUserByUsername(ctx, "t1", "alice")
			return err
		}},
		{"UpdateUser", http.MethodPut, "/users/1", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1})
			return err
		}},
		{"DeleteUser", http.MethodDelete, "/users/1", false, func(ctx context.Context, rt *Transport) error {
			return rt.DeleteUser(ctx, 1)
		}},
		{"ListUsers", http.MethodGet, "/users/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListUsers(ctx, &transport.ListUsersRequest{})
			return err
		}},
	}
	for name, call := range userStateChanges {
		cases = append(cases, failedAnswerCase{name, http.MethodPost, "/users/1/" + call.verb, false,
			func(ctx context.Context, rt *Transport) error { return call.do(rt, ctx, 1) }})
	}
	assertSurfacesAFailedAnswer(t, cases)
}

// TestRESTUser_NotFoundIsUserNotFoundError checks a 404 from every user
// operation that names one user becomes a UserNotFoundError naming that user.
func TestRESTUser_NotFoundIsUserNotFoundError(t *testing.T) {
	type notFoundCase struct {
		verb, path string
		want       transport.UserNotFoundError
		call       func(ctx context.Context, rt *Transport) error
	}
	byID := transport.UserNotFoundError{ID: 7}
	cases := map[string]notFoundCase{
		"GetUser": {http.MethodGet, "/users/7", byID, func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUser(ctx, 7)
			return err
		}},
		"GetUserByUsername": {http.MethodGet, "/users/by-username/missing",
			transport.UserNotFoundError{Lookup: transport.UserLookupKey("t1", "missing")},
			func(ctx context.Context, rt *Transport) error {
				_, err := rt.GetUserByUsername(ctx, "t1", "missing")
				return err
			}},
		"UpdateUser": {http.MethodPut, "/users/7", byID, func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 7})
			return err
		}},
		"DeleteUser": {http.MethodDelete, "/users/7", byID, func(ctx context.Context, rt *Transport) error {
			return rt.DeleteUser(ctx, 7)
		}},
	}
	for name, call := range userStateChanges {
		cases[name] = notFoundCase{http.MethodPost, "/users/7/" + call.verb, byID,
			func(ctx context.Context, rt *Transport) error { return call.do(rt, ctx, 7) }}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "not found", http.StatusNotFound)
			})
			err := c.call(context.Background(), rt)
			var nfe *transport.UserNotFoundError
			if !errors.As(err, &nfe) || *nfe != c.want {
				t.Fatalf("%s = %v, want %+v", name, err, c.want)
			}
		})
	}
}

// TestRESTUser_Closed checks every user operation refuses on a closed
// transport before it reaches the wire.
func TestRESTUser_Closed(t *testing.T) {
	calls := map[string]func(ctx context.Context, rt *Transport) error{
		"CreateUser": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateUser(ctx, &transport.CreateUserRequest{WithoutEmailLoginKey: true})
			return err
		},
		"GetUser": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUser(ctx, 1)
			return err
		},
		"GetUserByUsername": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetUserByUsername(ctx, "t1", "alice")
			return err
		},
		"UpdateUser": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1})
			return err
		},
		"DeleteUser": func(ctx context.Context, rt *Transport) error { return rt.DeleteUser(ctx, 1) },
		"ListUsers": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListUsers(ctx, &transport.ListUsersRequest{})
			return err
		},
	}
	for name, call := range userStateChanges {
		calls[name] = func(ctx context.Context, rt *Transport) error { return call.do(rt, ctx, 1) }
	}
	assertClosedRefusesBeforeTheWire(t, calls)
}
