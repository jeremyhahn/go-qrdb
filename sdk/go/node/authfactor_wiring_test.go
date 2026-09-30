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

//go:build !integration

package node

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// In-test fake authfactors.Factor — no real crypto, all in-memory.
// ---------------------------------------------------------------------------

// nodeTestFactor is a trivial Factor implementation used to populate a
// FactorRegistry for node wiring tests. It never performs real cryptographic
// operations: Enroll returns canned public credential metadata, Challenge
// returns a constant nonce, and Verify accepts only the literal response
// value "valid-sig".
type nodeTestFactor struct {
	name string
}

func newNodeTestFactor(name string) *nodeTestFactor { return &nodeTestFactor{name: name} }

func (f *nodeTestFactor) Name() string { return f.name }

func (f *nodeTestFactor) Enroll(_ context.Context, p *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "node-enroll-1",
		FactorName:   f.name,
		UserID:       p.UserID,
		TenantID:     p.TenantID,
		PublicKey:    []byte("fake-public-key"),
		Metadata:     map[string]string{"slot": "9a"},
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (f *nodeTestFactor) Challenge(_ context.Context, enrollment *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{
		ChallengeID:  "node-challenge-1",
		EnrollmentID: enrollment.EnrollmentID,
		Nonce:        []byte("test-nonce-32-bytes-long-padding"),
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	}, nil
}

func (f *nodeTestFactor) Verify(_ context.Context, _ *authfactors.Challenge, resp *authfactors.Response) error {
	if string(resp.Value) != "valid-sig" {
		return &authfactors.FactorNotFoundError{Name: f.name + ": bad response"}
	}
	return nil
}

var _ authfactors.Factor = (*nodeTestFactor)(nil)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newMemoryNodeWithServerOpts creates a standalone in-memory node with the
// given server.ServerOption slice and no NodeOptions. All protocol servers
// are disabled.
func newMemoryNodeWithServerOpts(t *testing.T, svrOpts ...server.ServerOption) *Node {
	t.Helper()
	n, err := NewStandaloneNode(context.Background(), newMemoryNodeCfg(t), svrOpts)
	require.NoError(t, err)
	require.NotNil(t, n)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// newPopulatedFactorRegistry returns a *authfactors.FactorRegistry that
// contains one nodeTestFactor registered under the given name.
func newPopulatedFactorRegistry(t *testing.T, name string) (*authfactors.FactorRegistry, *nodeTestFactor) {
	t.Helper()
	f := newNodeTestFactor(name)
	reg := authfactors.NewFactorRegistry()
	require.NoError(t, reg.Register(f))
	return reg, f
}

// ---------------------------------------------------------------------------
// AuthFactorWiringError
// ---------------------------------------------------------------------------

// TestAuthFactorWiringError_ErrorAndUnwrap verifies the typed error's
// Error() message and Unwrap() contract so callers can use errors.As / errors.Is.
func TestAuthFactorWiringError_ErrorAndUnwrap(t *testing.T) {
	t.Parallel()
	inner := &authfactors.FactorNotFoundError{Name: "fido2"}
	e := &AuthFactorWiringError{Err: inner}
	assert.Contains(t, e.Error(), "failed to wire AuthFactorService")
	assert.ErrorIs(t, e, inner)
}

// ---------------------------------------------------------------------------
// NewStandaloneNode with WithFactorRegistry
// ---------------------------------------------------------------------------

// TestNewStandaloneNode_WithFactorRegistry_PopulatesAuthFactorService verifies
// that constructing a standalone node with server.WithFactorRegistry results
// in a non-nil n.AuthFactorService() after the node is fully wired.
func TestNewStandaloneNode_WithFactorRegistry_PopulatesAuthFactorService(t *testing.T) {
	reg, _ := newPopulatedFactorRegistry(t, "piv")
	n := newMemoryNodeWithServerOpts(t, server.WithFactorRegistry(reg))

	require.NotNil(t, n.AuthFactorService(),
		"AuthFactorService() must be non-nil when constructed with WithFactorRegistry")
}

// TestNewStandaloneNode_NoFactorRegistry_NilAuthFactorService verifies that
// a node constructed without a FactorRegistry returns nil from
// AuthFactorService().
func TestNewStandaloneNode_NoFactorRegistry_NilAuthFactorService(t *testing.T) {
	n := newMemoryNodeWithServerOpts(t) // no server options

	assert.Nil(t, n.AuthFactorService(),
		"AuthFactorService() must be nil when no FactorRegistry or explicit service was supplied")
}

// ---------------------------------------------------------------------------
// End-to-end: Enroll → Challenge → Verify through the embedded transport
// ---------------------------------------------------------------------------

// TestAuthFactorService_EnrollChallengeVerify_EndToEnd proves that the
// AuthFactorService wired from WithFactorRegistry is reachable through the
// embedded transport's AuthFactors() surface and that the full
// Enroll → Challenge → Verify round-trip succeeds in-process.
//
// The context is bound to "tenant-1" — matching the request's TenantID field
// below — because the routed service refuses a request whose TenantID names
// a tenant other than the context's (go-qrdb#464): an unbound context resolves
// to the system tenant, and a request naming a different tenant there is
// exactly the spoof shape TestAuthFactorService_EnrollFactor_TenantIDMismatch
// covers below.
func TestAuthFactorService_EnrollChallengeVerify_EndToEnd(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "tenant-1")

	reg, _ := newPopulatedFactorRegistry(t, "piv")
	n := newMemoryNodeWithServerOpts(t, server.WithFactorRegistry(reg))

	require.NotNil(t, n.Client(), "node client must be non-nil after boot")
	af := n.Client().AuthFactors()
	require.NotNil(t, af, "AuthFactors() transport client must be non-nil")

	// Enroll a custodian with the fake PIV factor.
	enrollResp, err := af.EnrollFactor(ctx, &transport.EnrollFactorRequest{
		FactorName:   "piv",
		Subject:      "custodian-alice",
		TenantID:     "tenant-1",
		MethodParams: map[string]any{"slot": "9a"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, enrollResp.EnrollmentID, "enrollment ID must be set")
	assert.Equal(t, "piv", enrollResp.FactorName)
	assert.Equal(t, "custodian-alice", enrollResp.UserID)

	// Issue a challenge for the enrollment.
	chalResp, err := af.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{
		EnrollmentID: enrollResp.EnrollmentID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, chalResp.ChallengeID)

	// Verify with the correct response — the fake factor accepts "valid-sig".
	err = af.VerifyFactor(ctx, &transport.VerifyFactorRequest{
		FactorName:  "piv",
		ChallengeID: chalResp.ChallengeID,
		Value:       []byte("valid-sig"),
	})
	require.NoError(t, err, "VerifyFactor must succeed with the correct response value")
}

// TestAuthFactorService_VerifyFactor_BadResponse verifies that VerifyFactor
// returns a non-nil error when the response value is incorrect, exercising
// the rejection path of the factor implementation.
func TestAuthFactorService_VerifyFactor_BadResponse(t *testing.T) {
	ctx := context.Background()

	reg, _ := newPopulatedFactorRegistry(t, "piv")
	n := newMemoryNodeWithServerOpts(t, server.WithFactorRegistry(reg))
	af := n.Client().AuthFactors()

	enrollResp, err := af.EnrollFactor(ctx, &transport.EnrollFactorRequest{
		FactorName: "piv",
		Subject:    "custodian-bob",
	})
	require.NoError(t, err)

	chalResp, err := af.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{
		EnrollmentID: enrollResp.EnrollmentID,
	})
	require.NoError(t, err)

	err = af.VerifyFactor(ctx, &transport.VerifyFactorRequest{
		FactorName:  "piv",
		ChallengeID: chalResp.ChallengeID,
		Value:       []byte("WRONG"),
	})
	require.Error(t, err, "VerifyFactor must return an error for an incorrect response")
}

// ---------------------------------------------------------------------------
// Tenant routing (#464)
// ---------------------------------------------------------------------------

// TestAuthFactorService_RoutedAcrossTenants proves the AuthFactorService
// wired from WithFactorRegistry is tenant-routed rather than built over the
// node's bare KV servicer: an enrollment made under one tenant's context must
// not resolve a challenge issued under another's, and the enrolling tenant
// must still resolve it. A wiring that reverted to the flat construction
// would pass every other test in this file and fail only this one.
func TestAuthFactorService_RoutedAcrossTenants(t *testing.T) {
	reg, _ := newPopulatedFactorRegistry(t, "piv")
	n := newMemoryNodeWithServerOpts(t, server.WithFactorRegistry(reg))
	af := n.Client().AuthFactors()
	require.NotNil(t, af)

	acme := tenant.WithTenantID(context.Background(), "acme")
	globex := tenant.WithTenantID(context.Background(), "globex")

	enrollResp, err := af.EnrollFactor(acme, &transport.EnrollFactorRequest{
		FactorName: "piv",
		Subject:    "custodian-acme",
	})
	require.NoError(t, err)
	require.NotEmpty(t, enrollResp.EnrollmentID)

	_, err = af.ChallengeFactor(globex, &transport.ChallengeFactorRequest{
		EnrollmentID: enrollResp.EnrollmentID,
	})
	require.Error(t, err, "globex resolved a challenge for acme's enrollment")

	chalResp, err := af.ChallengeFactor(acme, &transport.ChallengeFactorRequest{
		EnrollmentID: enrollResp.EnrollmentID,
	})
	require.NoError(t, err, "acme could not resolve its own enrollment")
	require.NotEmpty(t, chalResp.ChallengeID)
}

// TestAuthFactorService_EnrollFactor_TenantIDMismatch proves the EMBEDDED
// transport surfaces the routing layer's tenant mismatch refusal end to end:
// a request whose context resolves to "acme" but whose TenantID field names
// "globex" is refused with *service.AuthFactorTenantMismatchError, and
// nothing is enrolled in either tenant. This is the exact spoof shape
// TestAuthFactorService_EnrollChallengeVerify_EndToEnd above binds its
// context tenant to match, rather than leaving mismatched.
func TestAuthFactorService_EnrollFactor_TenantIDMismatch(t *testing.T) {
	reg, _ := newPopulatedFactorRegistry(t, "piv")
	n := newMemoryNodeWithServerOpts(t, server.WithFactorRegistry(reg))
	af := n.Client().AuthFactors()
	require.NotNil(t, af)

	acme := tenant.WithTenantID(context.Background(), "acme")

	_, err := af.EnrollFactor(acme, &transport.EnrollFactorRequest{
		FactorName: "piv",
		Subject:    "custodian-spoof",
		TenantID:   "globex",
	})
	require.Error(t, err, "a request naming a tenant other than the context's must be refused")
	var mismatch *service.AuthFactorTenantMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, "globex", mismatch.Requested)
	assert.Equal(t, "acme", mismatch.Context)

	// Nothing was enrolled in either tenant.
	acmeFactors, err := af.ListFactors(acme, &transport.ListFactorsRequest{})
	require.NoError(t, err)
	assert.Empty(t, acmeFactors.Enrollments, "acme has an enrolment despite the refused request")
	globex := tenant.WithTenantID(context.Background(), "globex")
	globexFactors, err := af.ListFactors(globex, &transport.ListFactorsRequest{})
	require.NoError(t, err)
	assert.Empty(t, globexFactors.Enrollments, "globex has an enrolment despite the refused request")
}

// TestErrNoAuthFactorTenantRouting_WrappedByAuthFactorWiringError verifies
// the typed error's Error() message and Unwrap() contract, matching
// TestAuthFactorWiringError_ErrorAndUnwrap above. svcNode.StoreResolver is
// non-nil in every lifecycle.BuildServices composition this SDK builds
// today (barrier-backed or not), so this sentinel is not reachable through
// NewStandaloneNode; it is pinned directly so a lifecycle composition that
// stops wiring a resolver fails closed rather than silently reverting to a
// flat AuthFactorService.
func TestErrNoAuthFactorTenantRouting_WrappedByAuthFactorWiringError(t *testing.T) {
	t.Parallel()
	e := &AuthFactorWiringError{Err: ErrNoAuthFactorTenantRouting}
	assert.Contains(t, e.Error(), "failed to wire AuthFactorService")
	assert.ErrorIs(t, e, ErrNoAuthFactorTenantRouting)
}
