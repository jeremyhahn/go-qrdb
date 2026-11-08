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
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// nonStringArgs are the JSON shapes mcp.ParseString would coerce rather than
// refuse: an array and an object become "", a number and a boolean their
// text, and null "".
var nonStringArgs = map[string]any{
	"array":  []any{"1234"},
	"object": map[string]any{"pin": "1234"},
	"number": float64(1234),
	"bool":   true,
	"null":   nil,
}

// assertToolArgumentError fails unless err is a *ToolArgumentError naming arg.
func assertToolArgumentError(t *testing.T, err error, arg string) {
	t.Helper()
	var argErr *ToolArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("err = %v, want *ToolArgumentError for %s", err, arg)
	}
	if argErr.Argument != arg {
		t.Fatalf("ToolArgumentError.Argument = %q, want %q", argErr.Argument, arg)
	}
}

// assertArgumentResult fails unless res is a tool error result naming arg as
// not a string.
func assertArgumentResult(t *testing.T, res *mcp.CallToolResult, err error, arg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler err = %v, want a tool error result", err)
	}
	if res == nil || !res.IsError || len(res.Content) == 0 {
		t.Fatalf("result = %+v, want a tool error result", res)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("result content = %T, want mcp.TextContent", res.Content[0])
	}
	want := (&ToolArgumentError{Argument: arg, Reason: "must be a string"}).Error()
	if text.Text != want {
		t.Fatalf("result text = %q, want %q", text.Text, want)
	}
}

func TestStringArg_AbsentAndString(t *testing.T) {
	req := buildCallToolRequest(t, "t", map[string]any{"s": "v", "e": ""})
	for name, want := range map[string]struct {
		value   string
		present bool
	}{"s": {"v", true}, "e": {"", true}, "missing": {"", false}} {
		got, present, err := stringArg(req, name)
		if err != nil || got != want.value || present != want.present {
			t.Fatalf("stringArg(%q) = %q, %v, %v; want %q, %v, nil", name, got, present, err, want.value, want.present)
		}
	}
}

func TestStringArg_RefusesNonString(t *testing.T) {
	for shape, v := range nonStringArgs {
		t.Run(shape, func(t *testing.T) {
			req := buildCallToolRequest(t, "t", map[string]any{"s": v})
			got, present, err := stringArg(req, "s")
			assertToolArgumentError(t, err, "s")
			if got != "" || !present {
				t.Fatalf("stringArg = %q, %v; want \"\", true beside the error", got, present)
			}
		})
	}
}

func TestSecretArg_RefusesNonStringInEitherForm(t *testing.T) {
	for _, arg := range []string{"secret", "secret_b64"} {
		for shape, v := range nonStringArgs {
			t.Run(arg+"/"+shape, func(t *testing.T) {
				req := buildCallToolRequest(t, "t", map[string]any{arg: v})
				b, err := secretArg(req, "secret")
				assertToolArgumentError(t, err, arg)
				if b != nil {
					t.Fatalf("secretArg returned %x beside its error", b)
				}
			})
		}
	}
}

func TestSecretArg_TypeCheckPrecedesConflict(t *testing.T) {
	// Both forms present, one malformed: the shape is what is wrong, so the
	// shape is what is reported.
	req := buildCallToolRequest(t, "t", map[string]any{"secret": "s", "secret_b64": []any{}})
	_, err := secretArg(req, "secret")
	assertToolArgumentError(t, err, "secret_b64")
}

func TestSecretArg_EmptyStringIsNone(t *testing.T) {
	for _, arg := range []string{"secret", "secret_b64"} {
		req := buildCallToolRequest(t, "t", map[string]any{arg: ""})
		if b, err := secretArg(req, "secret"); err != nil || b != nil {
			t.Fatalf("secretArg(%s=\"\") = %x, %v; want nil, nil", arg, b, err)
		}
	}
}

func TestPINArg_RefusesNonStringInEitherForm(t *testing.T) {
	for _, arg := range []string{"so_pin", "so_pin_b64"} {
		for shape, v := range nonStringArgs {
			t.Run(arg+"/"+shape, func(t *testing.T) {
				req := buildCallToolRequest(t, "t", map[string]any{arg: v})
				pin, err := pinArg(req, "so_pin")
				assertToolArgumentError(t, err, arg)
				if pin != "" {
					t.Fatalf("pinArg returned %q beside its error", pin)
				}
			})
		}
	}
}

func TestInitializePINArgs_RefusesNonString(t *testing.T) {
	for _, arg := range []string{"so_pin", "so_pin_b64", "user_pin", "user_pin_b64"} {
		req := buildCallToolRequest(t, "t", map[string]any{arg: []any{"1234"}})
		so, user, err := initializePINArgs(req)
		assertToolArgumentError(t, err, arg)
		if so != "" || user != "" {
			t.Fatalf("initializePINArgs(%s) = %q, %q beside its error", arg, so, user)
		}
	}
}

func TestParseOptionalB64(t *testing.T) {
	req := buildCallToolRequest(t, "t", map[string]any{"ok": "aGk=", "bad": "!", "num": float64(1)})
	if b, err := parseOptionalB64(req, "ok"); err != nil || string(b) != "hi" {
		t.Fatalf("parseOptionalB64(ok) = %q, %v", b, err)
	}
	if b, err := parseOptionalB64(req, "absent"); err != nil || b != nil {
		t.Fatalf("parseOptionalB64(absent) = %q, %v", b, err)
	}
	if _, err := parseOptionalB64(req, "bad"); err == nil || !strings.Contains(err.Error(), "must be base64") {
		t.Fatalf("parseOptionalB64(bad) err = %v, want a base64 ToolArgumentError", err)
	}
	_, err := parseOptionalB64(req, "num")
	assertToolArgumentError(t, err, "num")
}

func TestBuildRaftConfig_RefusesNonStringDiscoverySecrets(t *testing.T) {
	for method, arg := range map[string]string{
		"static":    "static_shared_secret",
		"multicast": "multicast_shared_secret",
		"token":     "token_value",
	} {
		for shape, v := range nonStringArgs {
			t.Run(arg+"/"+shape, func(t *testing.T) {
				req := buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
					"name": "mydb", "cluster_mode": "distributed",
					"discovery_method": method, arg: v,
				})
				cfg, err := buildRaftConfig(req, updateRaftArgs)
				assertToolArgumentError(t, err, arg)
				if cfg != nil {
					t.Fatalf("buildRaftConfig returned %+v beside its error", cfg)
				}
			})
		}
	}
}

// The handlers below are built over a nil service: a malformed secret that
// got past the argument check would reach the service and panic, so the
// test fails rather than passing on a coerced value.

func TestCredentialHandlers_RefuseNonStringValue(t *testing.T) {
	cases := map[string]struct {
		handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		arg     string
	}{
		"store":  {handleCredentialStore(nil), "value"},
		"submit": {handleCredentialSubmit(nil), "value"},
		"rotate": {handleCredentialRotate(nil), "new_value"},
	}
	for name, tc := range cases {
		for shape, v := range nonStringArgs {
			t.Run(name+"/"+shape, func(t *testing.T) {
				req := buildCallToolRequest(t, "t", map[string]any{"name": "n", tc.arg: v})
				res, err := tc.handler(context.Background(), req)
				assertArgumentResult(t, res, err, tc.arg)
			})
		}
	}
}

func TestUserAuthFactorHandlers_RefuseNonStringCredential(t *testing.T) {
	cases := map[string]struct {
		handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args    map[string]any
		arg     string
	}{
		"add":         {handleUserAuthFactorAddTool(nil), map[string]any{"user_id": "1", "type": "t", "credential_b64": float64(1234)}, "credential_b64"},
		"add spki":    {handleUserAuthFactorAddTool(nil), map[string]any{"user_id": "1", "type": "t", "credential_b64": "aGk=", "spki_credential_b64": []any{}}, "spki_credential_b64"},
		"rotate":      {handleUserAuthFactorRotateTool(nil), map[string]any{"old_factor_id": "f", "type": "t", "credential_b64": map[string]any{}}, "credential_b64"},
		"rotate spki": {handleUserAuthFactorRotateTool(nil), map[string]any{"old_factor_id": "f", "type": "t", "credential_b64": "aGk=", "spki_credential_b64": true}, "spki_credential_b64"},
		"login":       {handleUserAuthFactorLoginTool(nil, nil), map[string]any{"type": "t", "lookup_key_b64": float64(1234)}, "lookup_key_b64"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := tc.handler(context.Background(), buildCallToolRequest(t, "t", tc.args))
			assertArgumentResult(t, res, err, tc.arg)
		})
	}
}

func TestTenantBarrierUnseal_RefusesNonStringKey(t *testing.T) {
	allow := func(context.Context, string, permission.Permission) error { return nil }
	handler := handleTenantBarrierUnseal(nil, allow)
	for shape, v := range nonStringArgs {
		t.Run(shape, func(t *testing.T) {
			req := buildCallToolRequest(t, "t", map[string]any{"tenant_id": "acme", "key": v})
			res, err := handler(context.Background(), req)
			assertArgumentResult(t, res, err, "key")
		})
	}
}

func TestInvitationRedeem_RefusesNonStringToken(t *testing.T) {
	handler := handleInvitationRedeem(nil)
	for shape, v := range nonStringArgs {
		t.Run(shape, func(t *testing.T) {
			req := buildCallToolRequest(t, "t", map[string]any{"tenant_id": "acme", "token": v})
			res, err := handler(context.Background(), req)
			assertArgumentResult(t, res, err, "token")
		})
	}
}
