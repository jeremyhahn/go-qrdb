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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// In-test stubs
// ---------------------------------------------------------------------------

// wiringTestAuthFactorSvc is a no-op AuthFactorServicer used to verify that
// an explicitly supplied service is not replaced by ensureAuthFactorService.
type wiringTestAuthFactorSvc struct{ service.AuthFactorServicer }

// ---------------------------------------------------------------------------
// Fake authfactors.Factor for wiring tests — no real crypto, no real I/O.
// ---------------------------------------------------------------------------

// fakeAuthFactor is a trivial authfactors.Factor implementation used only to
// populate a FactorRegistry in unit tests. It never performs real
// cryptographic operations.
type fakeAuthFactor struct{ name string }

func (f *fakeAuthFactor) Name() string { return f.name }

func (f *fakeAuthFactor) Enroll(_ context.Context, _ *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   f.name,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (f *fakeAuthFactor) Challenge(_ context.Context, _ *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{
		ChallengeID:  "challenge-1",
		EnrollmentID: "enroll-1",
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(time.Minute).UTC(),
	}, nil
}

func (f *fakeAuthFactor) Verify(_ context.Context, _ *authfactors.Challenge, _ *authfactors.Response) error {
	return nil
}

var _ authfactors.Factor = (*fakeAuthFactor)(nil)

// newPopulatedRegistry returns a FactorRegistry with one fake factor registered.
func newPopulatedRegistry() *authfactors.FactorRegistry {
	reg := authfactors.NewFactorRegistry()
	_ = reg.Register(&fakeAuthFactor{name: "fake"})
	return reg
}

// ---------------------------------------------------------------------------
// TestEnsureAuthFactorService_WithFactorRegistry
//
// Start() with a populated FactorRegistry and no explicit
// WithAuthFactorService must construct s.opts.authFactorSvc automatically.
// ---------------------------------------------------------------------------

func TestEnsureAuthFactorService_WithFactorRegistry(t *testing.T) {
	t.Parallel()

	reg := newPopulatedRegistry()
	cfg := testConfig(t, false, false) // no protocols → Start() exits quickly

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithFactorRegistry(reg),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with FactorRegistry: %v", err)
	}
	defer srv.Stop()

	if srv.opts.authFactorSvc == nil {
		t.Fatal("ensureAuthFactorService: authFactorSvc is nil after Start() with FactorRegistry")
	}
}

// ---------------------------------------------------------------------------
// TestEnsureAuthFactorService_NoRegistry
//
// Start() without any FactorRegistry must leave authFactorSvc nil.
// ---------------------------------------------------------------------------

func TestEnsureAuthFactorService_NoRegistry(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() without FactorRegistry: %v", err)
	}
	defer srv.Stop()

	if srv.opts.authFactorSvc != nil {
		t.Fatal("ensureAuthFactorService: authFactorSvc should be nil when no FactorRegistry provided")
	}
}

// ---------------------------------------------------------------------------
// TestEnsureAuthFactorService_ExplicitServiceRetained
//
// When both WithAuthFactorService and WithFactorRegistry are supplied, the
// explicit instance must be retained unchanged (not replaced).
// ---------------------------------------------------------------------------

func TestEnsureAuthFactorService_ExplicitServiceRetained(t *testing.T) {
	t.Parallel()

	explicit := &wiringTestAuthFactorSvc{}
	reg := newPopulatedRegistry()
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithAuthFactorService(explicit),
		WithFactorRegistry(reg),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with explicit service + registry: %v", err)
	}
	defer srv.Stop()

	if srv.opts.authFactorSvc != service.AuthFactorServicer(explicit) {
		t.Fatal("ensureAuthFactorService: explicit AuthFactorServicer was replaced by auto-constructed one")
	}
}

// ---------------------------------------------------------------------------
// TestResolveCryptoOptions_SurfacesAuthFactorService
//
// ResolveCryptoOptions must include an explicit AuthFactorServicer in the
// returned CryptoOptions when one was passed via WithAuthFactorService.
// ---------------------------------------------------------------------------

func TestResolveCryptoOptions_SurfacesAuthFactorService(t *testing.T) {
	t.Parallel()

	svc := &wiringTestAuthFactorSvc{}
	crypto := ResolveCryptoOptions(WithAuthFactorService(svc))

	if crypto.AuthFactorService == nil {
		t.Fatal("ResolveCryptoOptions: AuthFactorService is nil despite WithAuthFactorService")
	}
	if crypto.AuthFactorService != service.AuthFactorServicer(svc) {
		t.Fatal("ResolveCryptoOptions: AuthFactorService is not the supplied instance")
	}
}

// TestResolveCryptoOptions_AuthFactorServiceNilWhenAbsent verifies that
// ResolveCryptoOptions returns nil AuthFactorService when none is supplied.
func TestResolveCryptoOptions_AuthFactorServiceNilWhenAbsent(t *testing.T) {
	t.Parallel()

	crypto := ResolveCryptoOptions()
	if crypto.AuthFactorService != nil {
		t.Fatal("ResolveCryptoOptions: expected nil AuthFactorService when option not supplied")
	}
}
