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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

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
	cfg := testConfig(false, false)
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
// auto-constructs userAuthFactorSvc when a User service is configured and no
// explicit UserAuthFactorServicer was supplied.
func TestEnsureUserAuthFactorService_WithUserService(t *testing.T) {
	t.Parallel()

	cfg := testConfig(false, false) // no protocols → Start() exits quickly
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithUserService(&stubUserServiceForFactorWiring{}),
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

	cfg := testConfig(false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

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
	cfg := testConfig(false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(explicit),
		WithUserService(&stubUserServiceForFactorWiring{}),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with explicit service + user service: %v", err)
	}
	defer srv.Stop()

	if srv.opts.userAuthFactorSvc != service.UserAuthFactorServicer(explicit) {
		t.Fatal("ensureUserAuthFactorService: explicit UserAuthFactorServicer was replaced by auto-constructed one")
	}
}

// TestEnsureUserAuthFactorService_NilKVSkipsConstruction verifies that
// auto-construction is skipped (not an error) when no KV servicer is
// available, since service.NewUserAuthFactorService requires one.
func TestEnsureUserAuthFactorService_NilKVSkipsConstruction(t *testing.T) {
	t.Parallel()

	cfg := testConfig(false, false)
	srv := New(cfg, nil, &mockAdminService{},
		WithUserService(&stubUserServiceForFactorWiring{}),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with nil kvSvc: %v", err)
	}
	defer srv.Stop()

	if srv.opts.userAuthFactorSvc != nil {
		t.Fatal("ensureUserAuthFactorService: userAuthFactorSvc should stay nil when kvSvc is nil")
	}
}
