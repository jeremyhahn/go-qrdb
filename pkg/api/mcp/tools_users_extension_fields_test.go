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

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// testFactorLister is a minimal, UNTAGGED user.FactorLister double, local to
// this file rather than borrowed from tools_coverage_new_test.go's
// covMockUserAuthFactorSvc: that type lives behind a `codec_json && raft`
// build constraint, narrower than this file's (none), and a helper's
// constraint must be a superset of every caller's (AGENTS.md).
type testFactorLister struct {
	factors []*auth.AuthFactor
	err     error
}

func (f *testFactorLister) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.factors, nil
}

// ---------------------------------------------------------------------------
// userToRecord / projection helpers (go-qrdb#192, go-qrdb#197)
// ---------------------------------------------------------------------------

func TestUserToRecord_ProjectsExtensionFieldsAndOmitsFactorData(t *testing.T) {
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	u := &user.User{
		ID:       7,
		Username: "alice",
		TenantID: "acme",
		RoleBindings: []user.RoleBinding{
			{RoleID: "role-a", TenantID: "acme"},
		},
		Attributes: map[string]string{"department": "eng"},
		CreatedAt:  created,
		UpdatedAt:  created,
	}
	factorSvc := &testFactorLister{factors: []*auth.AuthFactor{
		{ID: "f1", Type: "fido2", RegisteredAt: created, Credential: []byte("do-not-leak")},
	}}

	rec := mustUserToRecord(t, factorSvc, u)
	if len(rec.RoleBindings) != 1 || rec.RoleBindings[0].RoleID != "role-a" || rec.RoleBindings[0].TenantID != "acme" {
		t.Errorf("RoleBindings = %+v, want [{role-a acme}]", rec.RoleBindings)
	}
	if rec.Attributes["department"] != "eng" {
		t.Errorf("Attributes = %+v, want department=eng", rec.Attributes)
	}
	if len(rec.Factors) != 1 || rec.Factors[0].ID != "f1" || rec.Factors[0].FactorName != "fido2" {
		t.Errorf("Factors = %+v, want [{f1 fido2 ...}]", rec.Factors)
	}
	if !rec.MFAEnabled {
		t.Error("MFAEnabled = false, want true (one factor enrolled)")
	}
	if len(rec.MFAMethods) != 1 || rec.MFAMethods[0] != "fido2" {
		t.Errorf("MFAMethods = %v, want [fido2]", rec.MFAMethods)
	}
	if rec.MFAEnrolledAt == "" {
		t.Error("MFAEnrolledAt is empty, want the factor's enrollment time")
	}

	// The Data blob must never appear anywhere in the marshaled record.
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "do-not-leak") {
		t.Fatalf("marshaled record leaks Factors[].Data: %s", data)
	}
}

func TestUserToRecord_Nil(t *testing.T) {
	if got := mustUserToRecord(t, nil, nil); got != nil {
		t.Errorf("userToRecord(nil) = %+v, want nil", got)
	}
}

func TestUserToRecord_NoExtensionFields(t *testing.T) {
	rec := mustUserToRecord(t, nil, &user.User{ID: 1, Username: "bare"})
	if rec.MFAEnabled || len(rec.MFAMethods) != 0 || rec.MFAEnrolledAt != "" {
		t.Errorf("MFA state = enabled=%v methods=%v enrolledAt=%q, want all-empty",
			rec.MFAEnabled, rec.MFAMethods, rec.MFAEnrolledAt)
	}
	if len(rec.RoleBindings) != 0 || len(rec.Attributes) != 0 || len(rec.Factors) != 0 {
		t.Errorf("extension fields = %+v, want all empty", rec)
	}
}

func TestCertBindingsToRecord_NoneIsNil(t *testing.T) {
	if got := certBindingsToRecord(nil); got != nil {
		t.Errorf("certBindingsToRecord(nil) = %v, want nil", got)
	}
}

func TestRoleBindingsToRecord_NoneIsNil(t *testing.T) {
	if got := roleBindingsToRecord(nil); got != nil {
		t.Errorf("roleBindingsToRecord(nil) = %v, want nil", got)
	}
}

func TestFactorsToRecord_NoActiveFactorsIsNil(t *testing.T) {
	if got := factorsToRecord(nil); got != nil {
		t.Errorf("factorsToRecord(nil) = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// parseRoleBindingsArg
// ---------------------------------------------------------------------------

func TestParseRoleBindingsArg_Absent(t *testing.T) {
	got, err := parseRoleBindingsArg(makeReq(map[string]any{}), "role_bindings")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for absent argument", got)
	}
}

func TestParseRoleBindingsArg_JSONStringEncoding(t *testing.T) {
	req := makeReq(map[string]any{
		"role_bindings": `[{"role_id":"role-a","tenant_id":"acme"}]`,
	})
	got, err := parseRoleBindingsArg(req, "role_bindings")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != (user.RoleBinding{RoleID: "role-a", TenantID: "acme"}) {
		t.Errorf("got %+v, want [{role-a acme}]", got)
	}
}

func TestParseRoleBindingsArg_NativeArrayEncoding(t *testing.T) {
	req := makeReq(map[string]any{
		"role_bindings": []any{
			map[string]any{"role_id": "role-a", "tenant_id": "acme"},
			map[string]any{"role_id": "role-b", "tenant_id": "beta"},
		},
	})
	got, err := parseRoleBindingsArg(req, "role_bindings")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []user.RoleBinding{{RoleID: "role-a", TenantID: "acme"}, {RoleID: "role-b", TenantID: "beta"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseRoleBindingsArg_EmptyStringIsError(t *testing.T) {
	_, err := parseRoleBindingsArg(makeReq(map[string]any{"role_bindings": ""}), "role_bindings")
	if err == nil {
		t.Fatal("expected error for empty string argument")
	}
	var target *ToolArgumentError
	if !errors.As(err, &target) {
		t.Fatalf("err = %T, want *ToolArgumentError", err)
	}
}

func TestParseRoleBindingsArg_MalformedJSONStringIsError(t *testing.T) {
	_, err := parseRoleBindingsArg(makeReq(map[string]any{"role_bindings": "not json"}), "role_bindings")
	if err == nil {
		t.Fatal("expected error for malformed JSON string")
	}
}

func TestParseRoleBindingsArg_WrongTypeIsError(t *testing.T) {
	_, err := parseRoleBindingsArg(makeReq(map[string]any{"role_bindings": 42}), "role_bindings")
	if err == nil {
		t.Fatal("expected error for a non-string, non-array argument")
	}
}

// ---------------------------------------------------------------------------
// parseAttributesArg
// ---------------------------------------------------------------------------

func TestParseAttributesArg_Absent(t *testing.T) {
	got, err := parseAttributesArg(makeReq(map[string]any{}), "attributes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for absent argument", got)
	}
}

func TestParseAttributesArg_JSONStringEncoding(t *testing.T) {
	req := makeReq(map[string]any{"attributes": `{"department":"eng"}`})
	got, err := parseAttributesArg(req, "attributes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["department"] != "eng" {
		t.Errorf("got %+v, want department=eng", got)
	}
}

func TestParseAttributesArg_NativeObjectEncoding(t *testing.T) {
	req := makeReq(map[string]any{
		"attributes": map[string]any{"department": "eng", "region": "us-east"},
	})
	got, err := parseAttributesArg(req, "attributes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["department"] != "eng" || got["region"] != "us-east" {
		t.Errorf("got %+v, want department=eng region=us-east", got)
	}
}

func TestParseAttributesArg_EmptyStringIsError(t *testing.T) {
	_, err := parseAttributesArg(makeReq(map[string]any{"attributes": ""}), "attributes")
	if err == nil {
		t.Fatal("expected error for empty string argument")
	}
}

func TestParseAttributesArg_MalformedJSONStringIsError(t *testing.T) {
	_, err := parseAttributesArg(makeReq(map[string]any{"attributes": "not json"}), "attributes")
	if err == nil {
		t.Fatal("expected error for malformed JSON string")
	}
}

func TestParseAttributesArg_NonStringValueIsError(t *testing.T) {
	req := makeReq(map[string]any{"attributes": map[string]any{"department": 42}})
	_, err := parseAttributesArg(req, "attributes")
	if err == nil {
		t.Fatal("expected error for a non-string attribute value")
	}
}

func TestParseAttributesArg_WrongTypeIsError(t *testing.T) {
	_, err := parseAttributesArg(makeReq(map[string]any{"attributes": 42}), "attributes")
	if err == nil {
		t.Fatal("expected error for a non-string, non-object argument")
	}
}

// ---------------------------------------------------------------------------
// handleUserUpdate: RoleBindings / Attributes wiring, clear flags
// ---------------------------------------------------------------------------

func TestUserUpdateTool_RoleBindingsAndAttributesReplace(t *testing.T) {
	var captured *user.UpdateUserRequest
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{ID: req.ID}, nil
		},
	}
	handler := handleUserUpdate(svc, nil, nil, &mockRoleService{getRoleFn: func(_ context.Context, id string) (*role.Role, error) {
		return &role.Role{ID: id, TenantID: "acme"}, nil
	}})

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":            "1",
		"role_bindings": `[{"role_id":"role-a","tenant_id":"acme"}]`,
		"attributes":    `{"department":"eng"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("UpdateUser was not called")
	}
	if len(captured.RoleBindings) != 1 || captured.RoleBindings[0].RoleID != "role-a" {
		t.Errorf("RoleBindings = %+v, want [{role-a acme}]", captured.RoleBindings)
	}
	if captured.Attributes["department"] != "eng" {
		t.Errorf("Attributes = %+v, want department=eng", captured.Attributes)
	}
}

func TestUserUpdateTool_ClearRoleBindingsAndAttributes(t *testing.T) {
	var captured *user.UpdateUserRequest
	svc := &mockUserService{
		updateUserFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
			captured = req
			return &user.User{ID: req.ID}, nil
		},
	}
	handler := handleUserUpdate(svc, nil, nil, nil)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"id":                  "1",
		"clear_role_bindings": true,
		"clear_attributes":    true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("UpdateUser was not called")
	}
	if captured.RoleBindings == nil || len(captured.RoleBindings) != 0 {
		t.Errorf("RoleBindings = %+v, want non-nil empty (clear)", captured.RoleBindings)
	}
	if captured.Attributes == nil || len(captured.Attributes) != 0 {
		t.Errorf("Attributes = %+v, want non-nil empty (clear)", captured.Attributes)
	}
}

func TestUserUpdateTool_RoleBindingsMalformedIsToolError(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserUpdate(svc, nil, nil, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":            "1",
		"role_bindings": "not json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected an error result for malformed role_bindings")
	}
}

func TestUserUpdateTool_AttributesMalformedIsToolError(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserUpdate(svc, nil, nil, nil)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":         "1",
		"attributes": "not json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected an error result for malformed attributes")
	}
}

// ---------------------------------------------------------------------------
// handleUserList: the Factors Data blob must never reach the wire (go-qrdb#192/#197)
// ---------------------------------------------------------------------------

func TestUserListTool_ProjectsFactorsMetadataOnly(t *testing.T) {
	svc := &mockUserService{
		listUsersFn: func(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return &user.ListUsersResponse{
				Users: []*user.User{{
					ID:       1,
					Username: "alice",
				}},
				Total: 1,
			}, nil
		},
	}
	factorSvc := &testFactorLister{factors: []*auth.AuthFactor{
		{ID: "f1", Type: "fido2", Credential: []byte("do-not-leak")},
	}}
	handler := handleUserList(svc, factorSvc)

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if strings.Contains(text, "do-not-leak") {
		t.Fatalf("qrdb_user_list leaked Factors[].Data: %s", text)
	}
	if !strings.Contains(text, "fido2") {
		t.Fatalf("qrdb_user_list dropped factor metadata entirely: %s", text)
	}
}
