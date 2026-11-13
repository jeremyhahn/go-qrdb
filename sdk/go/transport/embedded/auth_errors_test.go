// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// These tests exercise the Error() methods on the embedded-package
// auth error types so callers that log the messages get stable text.

func TestMissingAuthErrors_Messages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"rbac authorizer", &MissingRBACAuthorizerError{}, "RBAC authorizer is not configured"},
		{"role service", &MissingRoleServiceForRBACError{}, "role service is not configured"},
		{"factor service", &MissingUserAuthFactorServiceError{}, "user auth-factor service"},
		{"login resolver", &MissingLoginResolverError{}, "login resolver is not configured"},
		{"session issuer", &MissingSessionIssuerError{}, "session issuer is not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("error message %q does not contain %q", got, tc.want)
			}
		})
	}
}

// Closed-transport coverage on the lookup path.
func TestPermissionClient_LookupScope_ClosedTransport(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()
	_ = tr.Close()

	_, err := pc.LookupScope(context.Background(), "kv:put")
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

// factorTypeFromDomain default branch coverage.
// factorToTransport / factorToDomain nil coverage.
func TestFactorToTransport_Nil(t *testing.T) {
	got := factorToTransport(nil)
	if got.ID != "" {
		t.Errorf("expected zero-valued Factor for nil input, got %+v", got)
	}
}

// closed-transport coverage on Login.BeginLogin and FinishLogin.
func TestLoginClient_BeginLogin_Closed(t *testing.T) {
	tr := newLoginTestTransport(t,
		newMemUserAuthFactorService(),
		&memUserLookup{principals: map[uint64]*auth.Principal{}},
		newMemSessionIssuer(),
	)
	_ = tr.Close()
	_, err := tr.LoginClient().BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
}

func TestLoginClient_Logout_MissingIssuer(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = tr.LoginClient().Logout(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected MissingSessionIssuerError")
	}
	var miss *MissingSessionIssuerError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingSessionIssuerError, got %T", err)
	}
}

// Verify that Permission List on a transport with an open context is
// cancellation-safe.
func TestPermissionClient_List_ContextCancelled(t *testing.T) {
	tr := newPermissionTestTransport(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tr.PermissionClient().List(ctx)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

// Pool-mapping unknown error pass-through.
// Closed-transport coverage on RBAC operations.
func TestRBACClient_Authorize_Closed(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	_ = tr.Close()
	id := &transport.Identity{Subject: "a", TenantID: "acme"}
	err := tr.RBACClient().Authorize(context.Background(), id, "r", "a")
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

func TestRBACClient_ListPermissions_Closed(t *testing.T) {
	tr := newRBACTransport(t, rbacRoleSvc())
	_ = tr.Close()
	id := &transport.Identity{Subject: "a", TenantID: "acme"}
	_, err := tr.RBACClient().ListPermissions(context.Background(), id)
	if err == nil {
		t.Fatal("expected ClosedError")
	}
}

// Closed-transport coverage on factor operations.
func TestFactorClient_Enroll_Closed(t *testing.T) {
	tr := newFactorTransport(t)
	_ = tr.Close()
	err := tr.FactorClient().Enroll(context.Background(), 1, transport.Factor{Type: transport.FactorTypeFIDO2, Credential: []byte("c")})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
}

func TestFactorClient_List_Closed(t *testing.T) {
	tr := newFactorTransport(t)
	_ = tr.Close()
	_, err := tr.FactorClient().List(context.Background(), 1)
	if err == nil {
		t.Fatal("expected ClosedError")
	}
}

func TestFactorClient_Remove_Closed(t *testing.T) {
	tr := newFactorTransport(t)
	_ = tr.Close()
	err := tr.FactorClient().Remove(context.Background(), 1, "f-1")
	if err == nil {
		t.Fatal("expected ClosedError")
	}
}

func TestFactorClient_Verify_Closed(t *testing.T) {
	tr := newFactorTransport(t)
	_ = tr.Close()
	_, err := tr.FactorClient().Verify(context.Background(), 1, transport.VerifyChallenge{Type: transport.FactorTypeFIDO2, FactorID: "f-1"})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
}

// Verify on a missing factor surfaces FactorNotFoundError (mapping branch coverage).
func TestFactorClient_Verify_FactorNotFound(t *testing.T) {
	verifier := &mockVerifier{typ: transport.FactorTypeFIDO2}
	tr := newFactorTransport(t, WithFactorVerifier(verifier))
	_, err := tr.FactorClient().Verify(context.Background(), 1, transport.VerifyChallenge{
		FactorID:  "missing",
		Type:      transport.FactorTypeFIDO2,
		Nonce:     []byte("n"),
		Signature: []byte("s"),
	})
	if err == nil {
		t.Fatal("expected FactorNotFoundError")
	}
	var nf *transport.FactorNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("expected *transport.FactorNotFoundError, got %T", err)
	}
}

// Verify with a zero user id is rejected.
func TestFactorClient_Verify_ZeroUserID(t *testing.T) {
	tr := newFactorTransport(t)
	_, err := tr.FactorClient().Verify(context.Background(), 0, transport.VerifyChallenge{
		Type: transport.FactorTypeFIDO2, FactorID: "f-1",
	})
	if err == nil {
		t.Fatal("expected error for zero user id")
	}
}

// Verify with empty FactorID branch.
func TestFactorClient_Verify_EmptyFactorID(t *testing.T) {
	tr := newFactorTransport(t)
	_, err := tr.FactorClient().Verify(context.Background(), 1, transport.VerifyChallenge{
		Type: transport.FactorTypeFIDO2,
	})
	if err == nil {
		t.Fatal("expected FactorNotFoundError")
	}
}

// Verify against an invalid Type.
func TestFactorClient_Verify_InvalidType(t *testing.T) {
	tr := newFactorTransport(t)
	_, err := tr.FactorClient().Verify(context.Background(), 1, transport.VerifyChallenge{
		Type: transport.FactorType("bogus"), FactorID: "f-1",
	})
	if err == nil {
		t.Fatal("expected FactorTypeUnsupportedError")
	}
}

// WithFactorVerifier nil is a no-op (covers the early-return branch).
func TestWithFactorVerifier_Nil(t *testing.T) {
	tr := newFactorTransport(t, WithFactorVerifier(nil))
	if tr.factorVerifier(transport.FactorTypeFIDO2) != nil {
		t.Errorf("expected nil verifier registered for FIDO2")
	}
}

// Validate FactorClient.Enroll error path on transport.Closed before
// service nil-check.
func TestFactor_FactorVerifier_OverwriteSemantics(t *testing.T) {
	v1 := &mockVerifier{typ: transport.FactorTypeFIDO2}
	v2 := &mockVerifier{typ: transport.FactorTypeFIDO2}
	tr := newFactorTransport(t, WithFactorVerifier(v1), WithFactorVerifier(v2))
	got := tr.factorVerifier(transport.FactorTypeFIDO2)
	if got != v2 {
		t.Errorf("expected last-wins WithFactorVerifier semantics")
	}
}
