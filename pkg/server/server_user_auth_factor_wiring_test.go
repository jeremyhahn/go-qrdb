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

package server

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// newWiringStoreResolver returns the PRODUCTION tenant store resolver over an
// in-memory store. The wiring under test turns on whether a resolver is
// present, and on the routed service it produces actually working, so a stub
// resolver would test the stub.
func newWiringStoreResolver(t *testing.T) tenant.StoreResolver {
	t.Helper()
	store := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(store, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	resolver, err := tenant.NewStoreResolver(store, nil, tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	return resolver
}

// ---------------------------------------------------------------------------
// In-test stubs
// ---------------------------------------------------------------------------

// wiringTestUserAuthFactorSvc is a no-op UserAuthFactorServicer used to
// verify that an explicitly supplied service is not replaced by
// ensureUserAuthFactorService.
type wiringTestUserAuthFactorSvc struct{ service.UserAuthFactorServicer }

// stubUserServiceForFactorWiring is a minimal user.UserServicer used only to
// satisfy the ensureUserAuthFactorService gate (userSvc != nil). No method is
// ever invoked by these tests.
type stubUserServiceForFactorWiring struct{ user.UserServicer }

// ---------------------------------------------------------------------------
// WithUserAuthFactorService
// ---------------------------------------------------------------------------

// TestWithUserAuthFactorService verifies that WithUserAuthFactorService
// stores the provided UserAuthFactorServicer on serverOptions.
func TestWithUserAuthFactorService(t *testing.T) {
	t.Parallel()
	svc := &wiringTestUserAuthFactorSvc{}
	var opts serverOptions
	WithUserAuthFactorService(svc)(&opts)
	if opts.userAuthFactorSvc != service.UserAuthFactorServicer(svc) {
		t.Error("WithUserAuthFactorService did not store the provided service")
	}
}

// TestWithUserAuthFactorService_ViaNewServer verifies the option propagates
// through New().
func TestWithUserAuthFactorService_ViaNewServer(t *testing.T) {
	svc := &wiringTestUserAuthFactorSvc{}
	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithUserAuthFactorService(svc))
	if srv.opts.userAuthFactorSvc != service.UserAuthFactorServicer(svc) {
		t.Error("WithUserAuthFactorService not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// ensureUserAuthFactorService wiring
//
// This is the daemon-side analogue of sdk/go/node.go's buildLoginOpts: the
// M14 service is auto-constructed from the same KV servicer whenever a User
// service is configured and no explicit instance was supplied.
// ---------------------------------------------------------------------------

// TestEnsureUserAuthFactorService_WithUserService verifies that Start()
// auto-constructs userAuthFactorSvc when a User service AND a tenant store
// resolver are configured and no explicit UserAuthFactorServicer was supplied.
func TestEnsureUserAuthFactorService_WithUserService(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false) // no protocols → Start() exits quickly
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithUserService(&stubUserServiceForFactorWiring{}),
		WithTenantStoreResolver(newWiringStoreResolver(t)),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with WithUserService: %v", err)
	}
	defer srv.Stop()

	if srv.opts.userAuthFactorSvc == nil {
		t.Fatal("ensureUserAuthFactorService: userAuthFactorSvc is nil after Start() with WithUserService")
	}
}

// TestEnsureUserAuthFactorService_NoUserService verifies that Start() leaves
// userAuthFactorSvc nil when no User service is configured — mirroring
// sdk/go/node.go's buildEmbeddedOpts, which only wires the login ceremony
// when svcNode.User != nil.
func TestEnsureUserAuthFactorService_NoUserService(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA),
		WithTenantStoreResolver(newWiringStoreResolver(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() without WithUserService: %v", err)
	}
	defer srv.Stop()

	if srv.opts.userAuthFactorSvc != nil {
		t.Fatal("ensureUserAuthFactorService: userAuthFactorSvc should be nil when no User service configured")
	}
}

// TestEnsureUserAuthFactorService_ExplicitServiceRetained verifies that when
// both WithUserAuthFactorService and WithUserService are supplied, the
// explicit instance is retained unchanged (not replaced by auto-construction).
func TestEnsureUserAuthFactorService_ExplicitServiceRetained(t *testing.T) {
	t.Parallel()

	explicit := &wiringTestUserAuthFactorSvc{}
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(explicit),
		WithUserService(&stubUserServiceForFactorWiring{}),
		WithTenantStoreResolver(newWiringStoreResolver(t)),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with explicit service + user service: %v", err)
	}
	defer srv.Stop()

	if srv.opts.userAuthFactorSvc != service.UserAuthFactorServicer(explicit) {
		t.Fatal("ensureUserAuthFactorService: explicit UserAuthFactorServicer was replaced by auto-constructed one")
	}
}

// TestEnsureUserAuthFactorService_NoResolverFailsClosed verifies the
// go-qrdb#393 refusal: with a User service but NO tenant store resolver,
// Start() constructs no auth-factor service at all rather than one serving
// every tenant out of a single keyspace. The surface is withheld; it is never
// served unrouted.
func TestEnsureUserAuthFactorService_NoResolverFailsClosed(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithUserService(&stubUserServiceForFactorWiring{}),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() without a resolver: %v", err)
	}
	defer srv.Stop()

	if srv.opts.userAuthFactorSvc != nil {
		t.Fatal("ensureUserAuthFactorService: a service was constructed with no tenant routing")
	}
}

// TestEnsureUserAuthFactorService_RoutedServiceIsTenantScoped verifies the
// auto-constructed service is the ROUTED one, by exercising the boundary
// rather than the type: a factor enrolled under one tenant must not resolve a
// lookup made under another. A wiring that silently reverted to the unrouted
// service would satisfy a non-nil check and fail this.
func TestEnsureUserAuthFactorService_RoutedServiceIsTenantScoped(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithUserService(&stubUserServiceForFactorWiring{}),
		WithTenantStoreResolver(newWiringStoreResolver(t)),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer srv.Stop()

	svc := srv.opts.userAuthFactorSvc
	if svc == nil {
		t.Fatal("no auth-factor service was constructed")
	}
	cred := []byte("wiring-credential")
	acme := tenant.WithTenantID(context.Background(), "acme")
	globex := tenant.WithTenantID(context.Background(), "globex")
	if err := svc.Add(acme, 1, &auth.AuthFactor{
		ID: "wired", UserID: 1, Type: auth.AuthFactorMTLS, Credential: cred,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := svc.LookupByCredentialKey(globex, auth.AuthFactorMTLS, cred); err == nil {
		t.Fatal("the auto-constructed service resolved across tenants")
	}
	if _, err := svc.LookupByCredentialKey(acme, auth.AuthFactorMTLS, cred); err != nil {
		t.Fatalf("the enrolling tenant cannot resolve its own factor: %v", err)
	}
}
