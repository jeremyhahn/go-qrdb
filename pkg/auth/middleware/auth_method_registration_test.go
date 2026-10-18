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

package middleware

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// newVocabularyTestStore builds a real user store whose written policies are
// validated against known.
func newVocabularyTestStore(t *testing.T, known func(string) bool) user.UserServicer {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(3)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	store, err := user.NewStore(kvstore.NewMemoryKVStore(), gen, user.WithAuthMethodVocabulary(known))
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return store
}

// The vocabulary is the built-in set PLUS the names in THIS middleware's
// chain (the pluggable-auth-chain ruling), and it belongs to the instance: a
// second middleware in the same process, without the authenticator, does not
// gain it. That is what keeps two nodes in one process apart.
func TestAuthMiddleware_VocabularyIsItsOwnChain(t *testing.T) {
	withOAuth := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "OAuth"}))
	without := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: auth.MethodMTLS}))

	if !withOAuth.IsAuthMethod("oauth") || !withOAuth.IsAuthMethod(" OAUTH ") {
		t.Fatal("a middleware does not know its own chain's method")
	}
	if without.IsAuthMethod("oauth") {
		t.Fatal("a middleware without oauth in its chain knows it: the vocabulary leaked between instances")
	}
	if want := []string{auth.MethodMTLS, "oauth", auth.MethodPIV, auth.MethodWebAuthn}; !slices.Equal(withOAuth.AuthMethods(), want) {
		t.Errorf("AuthMethods() = %v, want %v", withOAuth.AuthMethods(), want)
	}
	for _, builtin := range auth.BuiltinAuthMethods() {
		if !without.IsAuthMethod(builtin) {
			t.Errorf("built-in %q missing from a chain that does not name it", builtin)
		}
	}

	var disabled *AuthMiddleware
	if !disabled.IsAuthMethod(auth.MethodPIV) || disabled.IsAuthMethod("oauth") {
		t.Error("a nil middleware (auth disabled) must answer for the built-ins only")
	}
	if !slices.Equal(disabled.AuthMethods(), auth.BuiltinAuthMethods()) {
		t.Errorf("nil AuthMethods() = %v", disabled.AuthMethods())
	}
}

// A blank authenticator name contributes nothing: "" is not a method a
// policy could name.
func TestAuthMiddleware_VocabularyIgnoresBlankNames(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "  "}), WithAuthenticator(nil))
	if m.IsAuthMethod("") || len(m.AuthMethods()) != len(auth.BuiltinAuthMethods()) {
		t.Fatalf("a blank or nil authenticator entered the vocabulary: %v", m.AuthMethods())
	}
}

// The user store validates written policies against the vocabulary it is
// given: ["mtls","oauth"] is accepted by a store wired to the middleware whose
// chain has oauth, and refused by one wired to a middleware without it.
func TestAuthMiddleware_VocabularyDrivesTheUserStore(t *testing.T) {
	ctx := context.Background()
	policy := []string{auth.MethodMTLS, "oauth"}
	withOAuth := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "oauth"}))
	without := newTestMiddleware(&mockAuthorizer{})

	u, err := newVocabularyTestStore(t, withOAuth.IsAuthMethod).CreateUser(ctx, &user.CreateUserRequest{
		Username: "olga", Email: "olga@example.com", TenantID: "t1", AuthMethods: policy,
	})
	if err != nil {
		t.Fatalf("CreateUser with a chain method: %v", err)
	}
	if err := CheckAuthMethod(u, "OAuth"); err != nil {
		t.Fatalf("the oauth authenticator is refused by the policy that permits it: %v", err)
	}

	_, err = newVocabularyTestStore(t, without.IsAuthMethod).CreateUser(ctx, &user.CreateUserRequest{
		Username: "olga", Email: "olga@example.com", TenantID: "t1", AuthMethods: policy,
	})
	var unknown *user.UnknownAuthMethodError
	if !errors.As(err, &unknown) || unknown.Method != "oauth" {
		t.Fatalf("a store without oauth in its chain: err = %v, want *UnknownAuthMethodError for oauth", err)
	}
}

// AuthMethodVocabulary is the late binding a node uses, because its user
// store is built before its middleware. Unbound, it is the built-ins; bound,
// it is that middleware's chain; bound to nil (auth disabled), the built-ins.
func TestAuthMethodVocabulary_LateBinding(t *testing.T) {
	var v AuthMethodVocabulary
	if !v.IsAuthMethod(auth.MethodWebAuthn) || v.IsAuthMethod("oauth") {
		t.Fatal("an unbound vocabulary must be the built-ins only")
	}
	v.Bind(newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "oauth"})))
	if !v.IsAuthMethod("oauth") {
		t.Fatal("the bound middleware's chain method is not in the vocabulary")
	}
	v.Bind(nil)
	if v.IsAuthMethod("oauth") || !v.IsAuthMethod(auth.MethodMTLS) {
		t.Fatal("binding nil must leave the built-ins only")
	}
}
