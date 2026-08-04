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

package secret_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// namedSecret is a defined string type. A bare string is not assignable to it,
// so resolving a map[string]namedSecret exercises the SetMapIndex path that
// previously panicked.
type namedSecret string

// TestConfigProcessor_NamedStringMapValue verifies the walker resolves secret
// references in maps whose element type is a named string type without panicking.
func TestConfigProcessor_NamedStringMapValue(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "cred", prefix: "cred://", value: []byte("resolved")})

	type cfg struct {
		Params map[string]namedSecret
	}
	c := &cfg{Params: map[string]namedSecret{"db": "cred://pass", "plain": "keepme"}}

	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), c)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", result.Resolved)
	}
	if c.Params["db"] != "resolved" {
		t.Errorf("Params[db] = %q, want resolved", c.Params["db"])
	}
	if c.Params["plain"] != "keepme" {
		t.Errorf("Params[plain] = %q, want keepme (unchanged)", c.Params["plain"])
	}
}

// TestConfigProcessor_LiteralSchemeStripped verifies that a literal:// reference
// is recognized and stripped to its verbatim value, even when the remainder
// looks like another scheme.
func TestConfigProcessor_LiteralSchemeStripped(t *testing.T) {
	reg := secret.NewResolverRegistry() // no resolvers; literal:// is built in

	type cfg struct{ Token string }
	c := &cfg{Token: "literal://cred://not-a-reference"}

	proc := secret.NewConfigProcessor(reg)
	result, err := proc.Process(context.Background(), c)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Resolved != 1 {
		t.Errorf("Resolved = %d, want 1", result.Resolved)
	}
	if c.Token != "cred://not-a-reference" {
		t.Errorf("Token = %q, want the literal:// prefix stripped", c.Token)
	}
}

// TestResolverRegistry_LiteralScheme verifies the registry's built-in handling
// of the literal:// scheme via Resolve and IsSecretURI.
func TestResolverRegistry_LiteralScheme(t *testing.T) {
	reg := secret.NewResolverRegistry()

	sv, err := reg.Resolve(context.Background(), "literal://value")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer sv.Clear()
	if sv.String() != "value" {
		t.Errorf("literal resolve = %q, want value", sv.String())
	}
	if sv.Strategy != secret.StrategyLiteral {
		t.Errorf("Strategy = %q, want literal", sv.Strategy)
	}
	if !reg.IsSecretURI("literal://x") {
		t.Error("IsSecretURI must recognize the literal:// scheme")
	}
	if reg.IsSecretURI("plain-value") {
		t.Error("IsSecretURI must be false for a bare value")
	}
}

// TestConfigProcessor_CyclicConfigDepthGuard verifies that a reference cycle is
// bounded by the depth guard and reported as a MaxDepthExceededError rather than
// overflowing the stack.
func TestConfigProcessor_CyclicConfigDepthGuard(t *testing.T) {
	type node struct {
		Name string
		Next *node
	}
	n := &node{Name: "plain"}
	n.Next = n // self-referential cycle

	proc := secret.NewConfigProcessor(secret.NewResolverRegistry())
	result, err := proc.Process(context.Background(), n)
	if err != nil {
		t.Fatalf("Process must not fail outright on a cycle: %v", err)
	}
	var depthErr *secret.MaxDepthExceededError
	found := false
	for _, e := range result.Errors {
		if errors.As(e, &depthErr) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a MaxDepthExceededError for a cyclic config graph, got %v", result.Errors)
	}
}

// TestNewTenantScopedCredentialRetrieve_NilSystemSvc verifies that a system-scope
// request with no system CredentialService fails closed rather than panicking.
func TestNewTenantScopedCredentialRetrieve_NilSystemSvc(t *testing.T) {
	fn := secret.NewTenantScopedCredentialRetrieve(nil, nil, nil, nil)
	_, err := fn(context.Background(), "name")
	if !errors.Is(err, secret.ErrNilSystemCredentialService) {
		t.Fatalf("expected ErrNilSystemCredentialService, got %v", err)
	}
}
