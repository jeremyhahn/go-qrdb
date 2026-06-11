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
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
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

// newMemoryNodeCfg returns a *qrdbconfig.Config for an in-memory standalone
// node with all protocols disabled so no ports are bound. It mirrors the
// pattern used by newMemoryNodeWithUser in login_wiring_test.go.
func newMemoryNodeCfg(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

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
func TestAuthFactorService_EnrollChallengeVerify_EndToEnd(t *testing.T) {
	ctx := context.Background()

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
