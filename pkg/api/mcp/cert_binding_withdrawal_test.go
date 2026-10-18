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

package mcp

// qrdb_user_remove_cert_binding — the withdrawal verb on the MCP surface
// (go-qrdb#518).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestHandleUserRemoveCertBinding_CarriesUserIDAndThumbprint is the
// reachability assertion: both tool arguments arrive at the service intact,
// with user_id parsed out of its string form.
func TestHandleUserRemoveCertBinding_CarriesUserIDAndThumbprint(t *testing.T) {
	const thumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var gotID uint64
	var gotThumbprint string
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			gotID, gotThumbprint = userID, tp
			return &user.User{ID: userID, Username: "alice"}, nil
		},
	}
	handler := handleUserRemoveCertBinding(svc, nil)

	req := buildCallToolRequest(t, "qrdb_user_remove_cert_binding", map[string]any{
		"user_id":    "1001",
		"thumbprint": thumbprint,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	if gotID != 1001 {
		t.Fatalf("userID = %d, want 1001", gotID)
	}
	if gotThumbprint != thumbprint {
		t.Fatalf("thumbprint = %q, want %q", gotThumbprint, thumbprint)
	}

	var got user.User
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &got); jerr != nil {
		t.Fatalf("unmarshal tool result: %v", jerr)
	}
	if got.ID != 1001 {
		t.Fatalf("result id = %d, want 1001", got.ID)
	}
}

// TestHandleUserRemoveCertBinding_RefusesMissingArguments covers both required
// arguments. A tool call missing either one must be refused rather than
// withdrawing an empty thumbprint from user zero.
func TestHandleUserRemoveCertBinding_RefusesMissingArguments(t *testing.T) {
	cases := map[string]map[string]any{
		"no user_id":    {"thumbprint": "AAAA"},
		"no thumbprint": {"user_id": "1001"},
		"unparseable id": {
			"user_id":    "not-a-number",
			"thumbprint": "AAAA",
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var called bool
			svc := &mockUserService{
				removeCertBindingFn: func(_ context.Context, _ uint64, _ string) (*user.User, error) {
					called = true
					return nil, nil
				},
			}
			handler := handleUserRemoveCertBinding(svc, nil)

			result, err := handler(context.Background(),
				buildCallToolRequest(t, "qrdb_user_remove_cert_binding", args))
			if err != nil {
				t.Fatalf("handler returned a transport error, want a tool error: %v", err)
			}
			if !result.IsError {
				t.Fatal("a malformed withdrawal was accepted")
			}
			if called {
				t.Fatal("a malformed withdrawal reached the service")
			}
		})
	}
}

// TestHandleUserRemoveCertBinding_PropagatesTheServiceRefusal keeps the
// store's refusal visible to the caller instead of reporting a success.
func TestHandleUserRemoveCertBinding_PropagatesTheServiceRefusal(t *testing.T) {
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			return nil, &user.CertBindingNotFoundError{UserID: userID, Thumbprint: tp}
		},
	}
	handler := handleUserRemoveCertBinding(svc, nil)

	_, err := handler(context.Background(),
		buildCallToolRequest(t, "qrdb_user_remove_cert_binding", map[string]any{
			"user_id":    "1001",
			"thumbprint": "AAAA",
		}))
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("err = %T(%v), want *ToolError", err, err)
	}
	var nf *user.CertBindingNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("the tool error dropped the store's refusal: %v", err)
	}
}

// TestToolUserRemoveCertBinding_DeclaresBothArgumentsRequired pins the schema
// an agent reads. A thumbprint declared optional is an invitation to call the
// tool without one.
func TestToolUserRemoveCertBinding_DeclaresBothArgumentsRequired(t *testing.T) {
	tool := toolUserRemoveCertBinding()
	if tool.Name != "qrdb_user_remove_cert_binding" {
		t.Fatalf("tool name = %q", tool.Name)
	}
	required := map[string]bool{}
	for _, r := range tool.InputSchema.Required {
		required[r] = true
	}
	for _, want := range []string{"user_id", "thumbprint"} {
		if !required[want] {
			t.Fatalf("%q is not declared required; required = %v", want, tool.InputSchema.Required)
		}
	}
}

// TestHandleUserAddCertBinding_HijackAttemptIsAlreadyExists pins the code the
// tool result carries for the store's most security-relevant refusal: a
// thumbprint already bound to ANOTHER user is CodeAlreadyExists, not
// CodeInternal (go-qrdb#536). The handler returns the typed error and the
// classifier renders it; both are asserted.
func TestHandleUserAddCertBinding_HijackAttemptIsAlreadyExists(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, userID uint64, tp string, _ int64) (*user.User, error) {
			return nil, &user.CertBindingConflictError{Thumbprint: tp, BoundUserID: 7, TargetUserID: userID}
		},
	}
	handler := handleUserAddCertBinding(svc, nil, nil)

	_, err := handler(context.Background(),
		buildCallToolRequest(t, "qrdb_user_add_cert_binding", map[string]any{
			"user_id":    "1001",
			"thumbprint": "AAAA",
		}))
	var conflict *user.CertBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %T(%v), want *user.CertBindingConflictError", err, err)
	}
	res := classifiedToolError("qrdb_user_add_cert_binding", err)
	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("tool result carries no error envelope")
	}
	if payload.Code != wire.CodeAlreadyExists {
		t.Fatalf("code = %v, want CodeAlreadyExists", payload.Code)
	}
	if !strings.Contains(payload.Message, "already bound to user 7") {
		t.Fatalf("message must name the user that holds the thumbprint, got %q", payload.Message)
	}
}

// TestUserTools_BootstrapAdminLockoutIsFailedPrecondition (go-qrdb#517): each
// user tool that can lock the bootstrap admin out fails with the store's typed
// refusal intact, and the classifier renders it CodeFailedPrecondition, as
// gRPC.
func TestUserTools_BootstrapAdminLockoutIsFailedPrecondition(t *testing.T) {
	lockout := func(reason user.AdminLockoutReason) error {
		return &user.BootstrapAdminLockoutError{UserID: 1001, Reason: reason}
	}
	svc := &mockUserService{
		removeCertBindingFn: func(context.Context, uint64, string) (*user.User, error) {
			return nil, lockout(user.AdminLockoutNoLiveBinding)
		},
		deleteUserFn:  func(context.Context, uint64) error { return lockout(user.AdminLockoutDelete) },
		disableUserFn: func(context.Context, uint64) error { return lockout(user.AdminLockoutLifecycle) },
		suspendUserFn: func(context.Context, uint64) error { return lockout(user.AdminLockoutLifecycle) },
		updateUserFn: func(context.Context, *user.UpdateUserRequest) (*user.User, error) {
			return nil, lockout(user.AdminLockoutRename)
		},
	}
	cases := map[string]struct {
		handler func(user.UserServicer) mcpserver.ToolHandlerFunc
		args    map[string]any
	}{
		"qrdb_user_remove_cert_binding": {func(svc user.UserServicer) mcpserver.ToolHandlerFunc {
			return handleUserRemoveCertBinding(svc, nil)
		}, map[string]any{"user_id": "1001", "thumbprint": "AAAA"}},
		"qrdb_user_delete":  {handleUserDelete, map[string]any{"id": "1001"}},
		"qrdb_user_disable": {handleUserDisable, map[string]any{"id": "1001"}},
		"qrdb_user_suspend": {handleUserSuspend, map[string]any{"id": "1001"}},
		"qrdb_user_update": {func(svc user.UserServicer) mcpserver.ToolHandlerFunc {
			return handleUserUpdate(svc, nil, nil, nil)
		}, map[string]any{"id": "1001", "username": "root"}},
	}
	for tool, tc := range cases {
		t.Run(tool, func(t *testing.T) {
			_, err := tc.handler(svc)(context.Background(), buildCallToolRequest(t, tool, tc.args))
			var refused *user.BootstrapAdminLockoutError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %T(%v), want the typed refusal", err, err)
			}
			payload, ok := wire.DecodeErrorPayload(classifiedToolError(tool, err).StructuredContent)
			if !ok || payload.Code != wire.CodeFailedPrecondition {
				t.Fatalf("payload = %+v (ok %v), want CodeFailedPrecondition", payload, ok)
			}
		})
	}
}

// TestHandleUserAddCertBinding_PastExpiryIsInvalidArgument (go-qrdb#517): an
// enrolment whose expiry has already passed is CodeInvalidArgument, as gRPC.
func TestHandleUserAddCertBinding_PastExpiryIsInvalidArgument(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, _ uint64, tp string, notAfter int64) (*user.User, error) {
			return nil, &user.CertBindingExpiredError{Thumbprint: tp, NotAfter: time.Unix(notAfter, 0).UTC()}
		},
	}
	const tool = "qrdb_user_add_cert_binding"
	_, err := handleUserAddCertBinding(svc, nil, nil)(context.Background(),
		buildCallToolRequest(t, tool, map[string]any{"user_id": "1001", "thumbprint": "AAAA", "not_after_unix": 1}))
	var expired *user.CertBindingExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("err = %T(%v), want *user.CertBindingExpiredError", err, err)
	}
	payload, ok := wire.DecodeErrorPayload(classifiedToolError(tool, err).StructuredContent)
	if !ok || payload.Code != wire.CodeInvalidArgument {
		t.Fatalf("payload = %+v (ok %v), want CodeInvalidArgument", payload, ok)
	}
}
