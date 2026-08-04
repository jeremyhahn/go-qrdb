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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// fakeResolver is a test double for SecretResolver.
// When prefix is non-empty, CanResolve matches via strings.HasPrefix;
// otherwise it returns canHandle verbatim.
type fakeResolver struct {
	scheme    string
	prefix    string // optional URI prefix for realistic scheme matching
	canHandle bool   // fallback when prefix is empty
	value     []byte
	err       error
}

func (r *fakeResolver) Scheme() string { return r.scheme }
func (r *fakeResolver) CanResolve(raw string) bool {
	if r.prefix != "" {
		return strings.HasPrefix(raw, r.prefix)
	}
	return r.canHandle
}
func (r *fakeResolver) Resolve(ctx context.Context, raw string) (*secret.SecretValue, error) {
	if r.err != nil {
		return nil, r.err
	}
	return secret.NewSecretValue(raw, r.value, secret.StrategyLiteral), nil
}

func TestResolverRegistry_DispatchOrder(t *testing.T) {
	reg := secret.NewResolverRegistry()
	first := &fakeResolver{scheme: "first", canHandle: true, value: []byte("first-value")}
	second := &fakeResolver{scheme: "second", canHandle: true, value: []byte("second-value")}
	reg.Register(first)
	reg.Register(second)

	sv, err := reg.Resolve(context.Background(), "test://x")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sv.String() != "first-value" {
		t.Errorf("expected first-value, got %q", sv.String())
	}
}

func TestResolverRegistry_LiteralFallback(t *testing.T) {
	reg := secret.NewResolverRegistry()
	// No resolvers registered — every value falls back to literal.
	raw := "plain-text-password"
	sv, err := reg.Resolve(context.Background(), raw)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sv.Strategy != secret.StrategyLiteral {
		t.Errorf("expected StrategyLiteral, got %s", sv.Strategy)
	}
	if sv.String() != raw {
		t.Errorf("expected %q, got %q", raw, sv.String())
	}
}

func TestResolverRegistry_LiteralFallback_WithNonMatchingResolver(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "kms", canHandle: false, value: []byte("x")})

	sv, err := reg.Resolve(context.Background(), "credential://db-pass")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sv.Strategy != secret.StrategyLiteral {
		t.Errorf("expected StrategyLiteral, got %s", sv.Strategy)
	}
}

func TestResolverRegistry_IsSecretURI_True(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "credential", canHandle: true})

	if !reg.IsSecretURI("credential://db-pass") {
		t.Error("expected IsSecretURI=true")
	}
}

func TestResolverRegistry_IsSecretURI_False(t *testing.T) {
	reg := secret.NewResolverRegistry()
	if reg.IsSecretURI("plain-value") {
		t.Error("expected IsSecretURI=false for empty registry")
	}
}

func TestResolverRegistry_NilCtx(t *testing.T) {
	reg := secret.NewResolverRegistry()
	_, err := reg.Resolve(nil, "any://thing") //nolint:staticcheck
	if !errors.Is(err, secret.ErrNilContext) {
		t.Errorf("expected ErrNilContext, got %v", err)
	}
}

func TestResolverRegistry_RegisterNil(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(nil) // must not panic
	if len(reg.Resolvers()) != 0 {
		t.Error("expected empty resolver list after registering nil")
	}
}

func TestResolverRegistry_Resolvers_Copy(t *testing.T) {
	reg := secret.NewResolverRegistry()
	reg.Register(&fakeResolver{scheme: "a", canHandle: true})
	reg.Register(&fakeResolver{scheme: "b", canHandle: true})

	copy1 := reg.Resolvers()
	copy2 := reg.Resolvers()
	if len(copy1) != 2 || len(copy2) != 2 {
		t.Fatalf("expected 2 resolvers, got %d and %d", len(copy1), len(copy2))
	}
	// Mutating one copy must not affect the registry.
	copy1[0] = nil
	if reg.Resolvers()[0] == nil {
		t.Error("mutating snapshot should not affect registry")
	}
}

func TestResolverRegistry_ResolutionFailedError(t *testing.T) {
	reg := secret.NewResolverRegistry()
	wantErr := errors.New("backend down")
	reg.Register(&fakeResolver{scheme: "kms", canHandle: true, err: wantErr})

	_, err := reg.Resolve(context.Background(), "kms://my-key")
	var rfe *secret.ResolutionFailedError
	if !errors.As(err, &rfe) {
		t.Fatalf("expected ResolutionFailedError, got %T: %v", err, err)
	}
	if rfe.Scheme != "kms" {
		t.Errorf("expected scheme=kms, got %q", rfe.Scheme)
	}
	if !errors.Is(err, wantErr) {
		t.Error("Unwrap chain must reach the underlying error")
	}
}
