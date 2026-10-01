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

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeCustodianFactor is a trivial authfactors.Factor used only to populate a
// Factors slice for buildAuthFactorServiceOption tests. It never performs
// real cryptographic operations.
type fakeCustodianFactor struct{ name string }

func (f *fakeCustodianFactor) Name() string { return f.name }

func (f *fakeCustodianFactor) Enroll(_ context.Context, p *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "daemon-enroll-" + p.UserID,
		FactorName:   f.name,
		UserID:       p.UserID,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (f *fakeCustodianFactor) Challenge(_ context.Context, e *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{
		ChallengeID:  "daemon-challenge-" + e.EnrollmentID,
		EnrollmentID: e.EnrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(time.Minute).UTC(),
	}, nil
}

func (f *fakeCustodianFactor) Verify(_ context.Context, _ *authfactors.Challenge, _ *authfactors.Response) error {
	return nil
}

var _ authfactors.Factor = (*fakeCustodianFactor)(nil)

// ---------------------------------------------------------------------------
// buildAuthFactorServiceOption (auth_factor.go)
// ---------------------------------------------------------------------------

// TestBuildAuthFactorServiceOption_Routed verifies that a resolver yields a
// working server.ServerOption carrying the TENANT-ROUTED service, and proves
// it on the ACTUAL instance the option installs — via
// server.ResolveCryptoOptions, the same accessor sdk/go/node uses to read a
// ServerOption's payload back out — not on a second, separately constructed
// service that happens to be correct. Asserting on a look-alike built beside
// the option under test proves nothing about the option: if
// buildAuthFactorServiceOption reverted to the flat, unrouted construction,
// a test built that way would still pass, because it never inspects what the
// option actually carries.
func TestBuildAuthFactorServiceOption_Routed(t *testing.T) {
	resolver, _ := newDaemonStoreResolver(t, "acme", "globex")
	factors := []authfactors.Factor{&fakeCustodianFactor{name: "fake"}}

	opt, err := buildAuthFactorServiceOption(resolver, factors)
	require.NoError(t, err)
	require.NotNil(t, opt)

	// No protocols enabled: server.New + Start()/Stop() must accept the
	// option and exit quickly, proving the constructed option is a valid
	// server.ServerOption rather than merely a non-nil closure.
	srv := server.New(&qrdbconfig.Config{}, fakeAuthFactorKV{}, nil, opt)
	require.NoError(t, srv.Start())
	defer srv.Stop()

	// The instance under test: read directly out of the option, exactly as
	// pkg/server's own Start() does before wiring it into every transport.
	svc := server.ResolveCryptoOptions(opt).AuthFactorService
	require.NotNil(t, svc, "buildAuthFactorServiceOption's option carries no AuthFactorService")

	acme := tenant.WithTenantID(context.Background(), "acme")
	globex := tenant.WithTenantID(context.Background(), "globex")

	enrollment, err := svc.EnrollFactor(acme, "fake", "daemon-custodian", authfactors.EnrollParams{})
	require.NoError(t, err)

	_, err = svc.ChallengeFactor(globex, enrollment.EnrollmentID)
	assert.Error(t, err, "the daemon-wired service resolved a challenge across tenants")

	_, err = svc.ChallengeFactor(acme, enrollment.EnrollmentID)
	assert.NoError(t, err, "the enrolling tenant could not resolve its own enrollment")
}

// TestBuildAuthFactorServiceOption_RejectsFlatConstruction is the negative
// control for TestBuildAuthFactorServiceOption_Routed: it builds the SAME
// assertion against the flat, unrouted service NewAuthFactorService would
// have produced over one of the resolver's underlying stores, and confirms
// that construction — the one buildAuthFactorServiceOption must NOT use —
// fails the cross-tenant check the routed test passes. This is what makes
// the positive test meaningful rather than vacuous: there exists a
// construction reachable from this same helper's dependencies that this
// suite proves wrong.
func TestBuildAuthFactorServiceOption_RejectsFlatConstruction(t *testing.T) {
	_, store := newDaemonStoreResolver(t, "acme", "globex")
	factors := []authfactors.Factor{&fakeCustodianFactor{name: "fake"}}

	flat, err := service.NewAuthFactorService(service.AuthFactorServiceConfig{
		Factors: factors,
		KV:      store,
	})
	require.NoError(t, err)

	acme := tenant.WithTenantID(context.Background(), "acme")
	globex := tenant.WithTenantID(context.Background(), "globex")

	enrollment, err := flat.EnrollFactor(acme, "fake", "daemon-custodian", authfactors.EnrollParams{})
	require.NoError(t, err)

	_, err = flat.ChallengeFactor(globex, enrollment.EnrollmentID)
	assert.NoError(t, err, "a flat service was expected to leak across tenants, proving the positive test discriminates")
}

// TestBuildAuthFactorServiceOption_NoResolver verifies the fail-closed
// refusal: with no resolver there is no tenant boundary to serve behind, so
// the wiring returns ErrNoTenantStoreResolverForAuthFactors rather than an
// unrouted service.
func TestBuildAuthFactorServiceOption_NoResolver(t *testing.T) {
	opt, err := buildAuthFactorServiceOption(nil, nil)
	assert.ErrorIs(t, err, ErrNoTenantStoreResolverForAuthFactors)
	assert.Nil(t, opt)
}
