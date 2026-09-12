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
	"testing"

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
	handler := handleUserRemoveCertBinding(svc)

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
			handler := handleUserRemoveCertBinding(svc)

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
	handler := handleUserRemoveCertBinding(svc)

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
