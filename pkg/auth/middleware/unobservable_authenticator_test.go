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

package middleware

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// hostBearerAuthenticator stands in for an embedder's bearer validator whose
// tokens are minted by the host's own login endpoints. unobservable is what
// it reports through auth.UnobservableMethodAuthenticator.
type hostBearerAuthenticator struct {
	identity     *auth.Identity
	unobservable bool
}

func (h *hostBearerAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return h.identity, nil
}

func (h *hostBearerAuthenticator) Name() string { return "host-bearer" }

func (h *hostBearerAuthenticator) MethodUnobservable() bool { return h.unobservable }

var _ auth.UnobservableMethodAuthenticator = (*hostBearerAuthenticator)(nil)

func restrictedUser() *mockUserServicer {
	return &mockUserServicer{user: &user.User{
		ID: 7, Username: "frank", TenantID: "t1",
		Lifecycle:   user.LifecycleActive,
		AuthMethods: []string{auth.MethodWebAuthn},
		RoleIDs:     []string{"viewer"},
	}}
}

// An authenticator that declares its method unobservable passes the chain for
// an account whose policy names other methods — the host enforced the policy
// when it minted the token — and every other step still runs: the identity is
// enriched from the account record.
func TestAuthenticate_UnobservableAuthenticatorSkipsTheMethodPolicy(t *testing.T) {
	m := New(&mockAuthorizer{},
		WithAuthenticator(&hostBearerAuthenticator{identity: &auth.Identity{Subject: "frank", TenantID: "t1"}, unobservable: true}),
		WithUserService(restrictedUser()))

	_, identity, err := m.authenticate(context.Background())
	if err != nil {
		t.Fatalf("an unobservable-method authenticator was refused: %v", err)
	}
	if len(identity.Roles) != 1 || identity.Roles[0] != "viewer" {
		t.Errorf("identity was not enriched from the account: roles %v", identity.Roles)
	}
}

// The declaration is the only way past the policy. The same authenticator
// reporting false, or one that does not implement the interface, is observed
// under its name and refused for an account that does not permit it.
func TestAuthenticate_ObservedBearerIsCheckedAgainstThePolicy(t *testing.T) {
	for name, authn := range map[string]auth.Authenticator{
		"reports observable": &hostBearerAuthenticator{identity: &auth.Identity{Subject: "frank", TenantID: "t1"}},
		"does not implement": &mockAuthenticator{name: "host-bearer", identity: &auth.Identity{Subject: "frank", TenantID: "t1"}},
	} {
		t.Run(name, func(t *testing.T) {
			m := New(&mockAuthorizer{}, WithAuthenticator(authn), WithUserService(restrictedUser()))
			_, _, err := m.authenticate(context.Background())
			var refused *AuthMethodNotPermittedError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %T (%v), want *AuthMethodNotPermittedError", err, err)
			}
			if refused.Method != "host-bearer" {
				t.Errorf("Method = %q, want the authenticator's name", refused.Method)
			}
		})
	}
}

// Declaring the method unobservable relaxes only the method policy. A disabled
// account is still refused.
func TestAuthenticate_UnobservableAuthenticatorStillRefusesADisabledAccount(t *testing.T) {
	users := restrictedUser()
	users.user.Lifecycle = user.LifecycleDisabled
	m := New(&mockAuthorizer{},
		WithAuthenticator(&hostBearerAuthenticator{identity: &auth.Identity{Subject: "frank", TenantID: "t1"}, unobservable: true}),
		WithUserService(users))

	_, _, err := m.authenticate(context.Background())
	var disabled *UserDisabledError
	if !errors.As(err, &disabled) {
		t.Fatalf("err = %T (%v), want *UserDisabledError", err, err)
	}
}

// injectAuthorization carries gRPC's authorization metadata through the same
// carrier REST and MCP fill, taking the first value as Header.Get does.
func TestInjectAuthorization_CarriesTheFirstValue(t *testing.T) {
	md := metadata.Pairs("authorization", "Bearer first", "authorization", "Bearer second")
	ctx := injectAuthorization(metadata.NewIncomingContext(context.Background(), md))
	if got := auth.HTTPAuthorizationFromContext(ctx); got != "Bearer first" {
		t.Fatalf("carrier = %q, want %q", got, "Bearer first")
	}
}

// Without the metadata nothing is stored, so a bearer authenticator sees no
// credential and declines.
func TestInjectAuthorization_AbsentMetadataStoresNothing(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no metadata":    context.Background(),
		"other metadata": metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-qrdb-database", "db1")),
	} {
		if got := auth.HTTPAuthorizationFromContext(injectAuthorization(ctx)); got != "" {
			t.Errorf("%s: carrier = %q, want empty", name, got)
		}
	}
}
