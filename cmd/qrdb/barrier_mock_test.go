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

// The barrier CLI test double and its root builder carry NO build constraint:
// barrier_cmd_test.go (!integration) and cluster_seal_test.go (unconstrained)
// both use them, and a helper's constraint must be a superset of every
// caller's (CLAUDE.md, "Build constraints on test files").

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock BarrierServicer
// ---------------------------------------------------------------------------

// mockBarrierService implements service.BarrierServicer with configurable
// function fields for per-test behavior injection.
type mockBarrierService struct {
	initializeFn       func(ctx context.Context, secret []byte) error
	unsealFn           func(ctx context.Context, secret []byte) error
	changePassphraseFn func(ctx context.Context, current, next []byte) error
	sealFn             func(ctx context.Context) error
	statusFn           func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn func(ctx context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	genRecoveryKeysFn  func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	deleteRecoveryFn   func(ctx context.Context) error
	hasRecoveryKeysFn  func(ctx context.Context) (bool, error)

	officerRegisterFn    func(ctx context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn        func(ctx context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn    func(ctx context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn func(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	quorumBeginFn        func(ctx context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error)
	quorumSubmitFn       func(ctx context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error)
	rekeyStartFn         func(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy) (*service.QuorumRoundInfo, error)
	officerRevokeFn      func(ctx context.Context, domain seal.QuorumDomain, username string, threshold int) (*service.QuorumRoundInfo, error)
	clusterUnsealFn      func(ctx context.Context) (*clusterseal.Report, error)
}

var _ service.BarrierServicer = (*mockBarrierService)(nil)

// errBarrierMockUnset answers a holder method the test did not configure, so
// an unexpected call fails loudly instead of answering a zero value.
var errBarrierMockUnset = errors.New("mockBarrierService: holder method not configured by this test")

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierChangePassphrase(ctx context.Context, current, next []byte) error {
	if m.changePassphraseFn != nil {
		return m.changePassphraseFn(ctx, current, next)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.sealFn != nil {
		return m.sealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.BarrierStatusInfo{
		Sealed:         false,
		Strategy:       "passphrase",
		HardwareBacked: false,
		InitializedAt:  time.Now(),
	}, nil
}

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, threshold, total, policy)
	}
	return &service.ShamirInitInfo{ShareCount: total, Threshold: threshold, TotalShares: total}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.genRecoveryKeysFn != nil {
		return m.genRecoveryKeysFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{ShareCount: threshold + 1, Threshold: threshold, Total: threshold + 1}, nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryFn != nil {
		return m.deleteRecoveryFn(ctx)
	}
	return nil
}

// BarrierInitializePrecondition admits every initialize: the double's
// initialize paths answer for themselves through their canned results.
func (m *mockBarrierService) BarrierInitializePrecondition(_ context.Context) error { return nil }

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return false, nil
}

func (m *mockBarrierService) BarrierOfficerRegister(ctx context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, domain, req)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierOfficerList(ctx context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx, domain)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierShareClaimBegin(ctx context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.shareClaimBeginFn != nil {
		return m.shareClaimBeginFn(ctx, domain, username, thumbprint)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierShareClaimComplete(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.shareClaimCompleteFn != nil {
		return m.shareClaimCompleteFn(ctx, domain, username, nonceHex, signature, thumbprint)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierQuorumBegin(ctx context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if m.quorumBeginFn != nil {
		return m.quorumBeginFn(ctx, req)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierQuorumSubmit(ctx context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	if m.quorumSubmitFn != nil {
		return m.quorumSubmitFn(ctx, req)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierRekeyStart(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, _ string) (*service.QuorumRoundInfo, error) {
	if m.rekeyStartFn != nil {
		return m.rekeyStartFn(ctx, domain, threshold, policy)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierOfficerRevoke(ctx context.Context, domain seal.QuorumDomain, username string, threshold int, _ string) (*service.QuorumRoundInfo, error) {
	if m.officerRevokeFn != nil {
		return m.officerRevokeFn(ctx, domain, username, threshold)
	}
	return nil, errBarrierMockUnset
}

// BarrierClusterUnseal answers as a node without cluster sealing does, unless
// clusterUnsealFn is set.
func (m *mockBarrierService) BarrierClusterUnseal(ctx context.Context) (*clusterseal.Report, error) {
	if m.clusterUnsealFn != nil {
		return m.clusterUnsealFn(ctx)
	}
	return nil, clusterseal.ErrInactive
}

// newBarrierTestRoot builds a root command with an injected barrier service
// and a real PIN backend, as every composer wires one.
func newBarrierTestRoot(t *testing.T, svc service.BarrierServicer) *cobra.Command {
	t.Helper()
	return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBarrierService(svc), embedded.WithPINBackend(pintest.NewBackend(t)))
}
