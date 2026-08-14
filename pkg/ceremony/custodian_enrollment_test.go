// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ceremony

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// ---------------------------------------------------------------------------
// Stubs / fakes
// ---------------------------------------------------------------------------

// fakeCustodianSvc implements CustodianServicer.
type fakeCustodianSvc struct {
	custodians   map[string]*custodian.Custodian
	registerErr  error
	getCustodian func(id string) (*custodian.Custodian, error)
}

func newFakeCustodianSvc() *fakeCustodianSvc {
	return &fakeCustodianSvc{custodians: make(map[string]*custodian.Custodian)}
}

func (f *fakeCustodianSvc) RegisterCustodian(_ context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	c := &custodian.Custodian{
		ID:       req.Username + "-id",
		Username: req.Username,
		TenantID: req.TenantID,
		Method:   req.Method,
	}
	f.custodians[c.ID] = c
	return &custodian.RegisterResponse{
		Custodian:      c,
		SealedEnvelope: &custodian.SealedEnvelope{},
	}, nil
}

func (f *fakeCustodianSvc) GetCustodian(_ context.Context, id string) (*custodian.Custodian, error) {
	if f.getCustodian != nil {
		return f.getCustodian(id)
	}
	c, ok := f.custodians[id]
	if !ok {
		return nil, errors.New("custodian not found")
	}
	return c, nil
}

func (f *fakeCustodianSvc) RevokeCustodian(_ context.Context, id string) error {
	c, ok := f.custodians[id]
	if !ok {
		return errors.New("custodian not found")
	}
	now := time.Now()
	c.RevokedAt = &now
	return nil
}

// fakeFactorVerifier implements AuthFactorVerifier.
type fakeFactorVerifier struct {
	err error
}

func (f *fakeFactorVerifier) VerifyFactor(_ context.Context, _ string, _ string, _ *authfactors.Response) error {
	return f.err
}

// fakeBarrierUnsealer implements BarrierUnsealer.
type fakeBarrierUnsealer struct {
	unsealErr error
	called    bool
}

func (f *fakeBarrierUnsealer) UnsealWithKey(_ context.Context, _ []byte) error {
	f.called = true
	return f.unsealErr
}

// fakeAuditLogger implements audit.AuditLogger.
type fakeAuditLogger struct {
	events []audit.AuditEvent
}

func (f *fakeAuditLogger) Emit(_ context.Context, ev audit.AuditEvent) {
	f.events = append(f.events, ev)
}

func (f *fakeAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (f *fakeAuditLogger) Close() error { return nil }

// newCeremonyServiceWithExt builds a CeremonyService already wired with an extension.
func newCeremonyServiceWithExt(t *testing.T, ext *CeremonyExtension) *CeremonyService {
	t.Helper()
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	svc.SetExtension(ext)
	return svc
}

// defaultExt returns a CeremonyExtension with a functional fake custodian service
// and a passing factor verifier. The accumulator threshold defaults to 2.
func defaultExt(custSvc *fakeCustodianSvc) *CeremonyExtension {
	return &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
	}
}

// ---------------------------------------------------------------------------
// unsealAccumulator
// ---------------------------------------------------------------------------

func TestUnsealAccumulator_SubmitAndCollect(t *testing.T) {
	acc := newUnsealAccumulator(2)

	submitted1, reached1 := acc.submit([]byte("share-one"))
	assert.Equal(t, 1, submitted1)
	assert.False(t, reached1, "threshold not yet reached after first share")

	submitted2, reached2 := acc.submit([]byte("share-two"))
	assert.Equal(t, 2, submitted2)
	assert.True(t, reached2, "threshold should be reached after second share")

	shares := acc.collect()
	assert.Len(t, shares, 2, "collect must return all submitted shares")
	assert.NotEmpty(t, shares[0])
	assert.NotEmpty(t, shares[1])
}

func TestUnsealAccumulator_ThresholdReachedOnlyOnce(t *testing.T) {
	acc := newUnsealAccumulator(1)

	_, reached1 := acc.submit([]byte("share-a"))
	assert.True(t, reached1)

	_, reached2 := acc.submit([]byte("share-b"))
	assert.False(t, reached2, "second submit must not re-trigger threshold")
}

func TestUnsealAccumulator_CollectAfterThreshold(t *testing.T) {
	acc := newUnsealAccumulator(3)
	acc.submit([]byte("x"))
	acc.submit([]byte("y"))
	acc.submit([]byte("z"))

	out := acc.collect()
	assert.Len(t, out, 3)
}

// ---------------------------------------------------------------------------
// SetExtension
// ---------------------------------------------------------------------------

func TestSetExtension_InitializesFactorChallenges(t *testing.T) {
	svc := newCeremonyServiceWithExt(t, defaultExt(newFakeCustodianSvc()))
	svc.mu.Lock()
	defer svc.mu.Unlock()
	assert.NotNil(t, svc.ext.factorChallenges, "SetExtension must initialise factorChallenges map")
}

func TestSetExtension_NilExtension(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	// Setting nil must not panic.
	svc.SetExtension(nil)
	assert.Nil(t, svc.ext)
}

// ---------------------------------------------------------------------------
// EnrollCustodianViaFactor
// ---------------------------------------------------------------------------

func TestEnrollCustodianViaFactor_NilExtension(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.EnrollCustodianViaFactor(context.Background(), EnrollCustodianRequest{
		Username: "alice",
	})
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestEnrollCustodianViaFactor_NilCustodianSvc(t *testing.T) {
	svc := newCeremonyServiceWithExt(t, &CeremonyExtension{})

	_, err := svc.EnrollCustodianViaFactor(context.Background(), EnrollCustodianRequest{
		Username: "alice",
	})
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestEnrollCustodianViaFactor_Success(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc: custSvc,
		AuditLogger:  auditLog,
		Threshold:    2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	resp, err := svc.EnrollCustodianViaFactor(context.Background(), EnrollCustodianRequest{
		SOIdentity: "so-1",
		TenantID:   "tenant-a",
		Username:   "alice",
		Method:     custodian.FactorMethodSoftware,
		ShareIndex: 1,
		ShareBytes: []byte("fake-share"),
	})
	require.NoError(t, err)
	assert.NotNil(t, resp.Custodian)
	assert.Equal(t, "alice", resp.Custodian.Username)
	assert.NotNil(t, resp.SealedEnvelope)

	// Audit log must record the success.
	require.NotEmpty(t, auditLog.events)
	assert.Equal(t, audit.OutcomeSuccess, auditLog.events[len(auditLog.events)-1].Outcome)
}

func TestEnrollCustodianViaFactor_RegisterError_EmitsFailureAudit(t *testing.T) {
	registerErr := errors.New("duplicate custodian")
	custSvc := newFakeCustodianSvc()
	custSvc.registerErr = registerErr
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc: custSvc,
		AuditLogger:  auditLog,
		Threshold:    2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.EnrollCustodianViaFactor(context.Background(), EnrollCustodianRequest{
		SOIdentity: "so-1",
		Username:   "bob",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, registerErr)

	require.NotEmpty(t, auditLog.events)
	last := auditLog.events[len(auditLog.events)-1]
	assert.Contains(t, last.Outcome, audit.OutcomeFailure)
}

func TestEnrollCustodianViaFactor_RegisterError_NoAuditLogger(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.registerErr = errors.New("register failed")
	ext := &CeremonyExtension{CustodianSvc: custSvc, Threshold: 2}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.EnrollCustodianViaFactor(context.Background(), EnrollCustodianRequest{Username: "carol"})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// ClaimShareChallenge
// ---------------------------------------------------------------------------

func TestClaimShareChallenge_NilExtension(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.ClaimShareChallenge(context.Background(), "custodian-1")
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestClaimShareChallenge_EmptyCustodianID(t *testing.T) {
	svc := newCeremonyServiceWithExt(t, defaultExt(newFakeCustodianSvc()))
	_, err := svc.ClaimShareChallenge(context.Background(), "")
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestClaimShareChallenge_CustodianNotFound(t *testing.T) {
	custSvc := newFakeCustodianSvc() // no custodians registered
	svc := newCeremonyServiceWithExt(t, defaultExt(custSvc))

	_, err := svc.ClaimShareChallenge(context.Background(), "unknown-id")
	assert.Error(t, err)
}

func TestClaimShareChallenge_RevokedCustodian(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	now := time.Now()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", RevokedAt: &now}

	svc := newCeremonyServiceWithExt(t, defaultExt(custSvc))
	_, err := svc.ClaimShareChallenge(context.Background(), "c1")
	assert.ErrorIs(t, err, ErrCustodianRevoked)
}

func TestClaimShareChallenge_Success(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", Username: "alice"}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{CustodianSvc: custSvc, AuditLogger: auditLog, Threshold: 2}
	svc := newCeremonyServiceWithExt(t, ext)

	ch, err := svc.ClaimShareChallenge(context.Background(), "c1")
	require.NoError(t, err)
	assert.Equal(t, "c1", ch.CustodianID)
	assert.Len(t, ch.Nonce, custodianNonceSize)
	assert.True(t, ch.ExpiresAt.After(time.Now()))

	// Audit log must have a success event.
	require.NotEmpty(t, auditLog.events)
	assert.Equal(t, audit.OutcomeSuccess, auditLog.events[len(auditLog.events)-1].Outcome)
}

func TestClaimShareChallenge_Success_NoAuditLogger(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	ext := &CeremonyExtension{CustodianSvc: custSvc, Threshold: 2}
	svc := newCeremonyServiceWithExt(t, ext)

	ch, err := svc.ClaimShareChallenge(context.Background(), "c1")
	require.NoError(t, err)
	assert.NotEmpty(t, ch.Nonce)
}

// ---------------------------------------------------------------------------
// VerifyClaimAndDeliver
// ---------------------------------------------------------------------------

func TestVerifyClaimAndDeliver_NilExtension(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", nil)
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestVerifyClaimAndDeliver_EmptyCustodianID(t *testing.T) {
	svc := newCeremonyServiceWithExt(t, defaultExt(newFakeCustodianSvc()))
	_, err := svc.VerifyClaimAndDeliver(context.Background(), "", "ch1", nil)
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestVerifyClaimAndDeliver_CustodianNotFound(t *testing.T) {
	svc := newCeremonyServiceWithExt(t, defaultExt(newFakeCustodianSvc()))
	_, err := svc.VerifyClaimAndDeliver(context.Background(), "missing", "ch1", nil)
	assert.Error(t, err)
}

func TestVerifyClaimAndDeliver_RevokedCustodian(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	now := time.Now()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", RevokedAt: &now}

	svc := newCeremonyServiceWithExt(t, defaultExt(custSvc))
	_, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", nil)
	assert.ErrorIs(t, err, ErrCustodianRevoked)
}

func TestVerifyClaimAndDeliver_AlreadyDelivered(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", ShareClaimed: true}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		AuditLogger:    auditLog,
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", nil)
	assert.ErrorIs(t, err, ErrShareAlreadyDelivered)
}

func TestVerifyClaimAndDeliver_ChallengeNotFound(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	svc := newCeremonyServiceWithExt(t, defaultExt(custSvc))

	// No challenge was issued for c1.
	_, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", nil)
	assert.ErrorIs(t, err, ErrChallengeNotFound)
}

func TestVerifyClaimAndDeliver_ChallengeExpired(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
		factorChallenges: map[string]*CustodianFactorChallenge{
			"c1": {
				CustodianID: "c1",
				Nonce:       []byte("nonce"),
				ExpiresAt:   time.Now().Add(-time.Minute), // already expired
			},
		},
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", nil)
	assert.ErrorIs(t, err, ErrChallengeExpired)
}

func TestVerifyClaimAndDeliver_FactorVerifyFails(t *testing.T) {
	verifyErr := errors.New("bad factor")
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{err: verifyErr},
		AuditLogger:    auditLog,
		Threshold:      2,
		factorChallenges: map[string]*CustodianFactorChallenge{
			"c1": {
				CustodianID: "c1",
				Nonce:       []byte("nonce"),
				ExpiresAt:   time.Now().Add(time.Minute),
			},
		},
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", &authfactors.Response{})
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)

	require.NotEmpty(t, auditLog.events)
	last := auditLog.events[len(auditLog.events)-1]
	assert.Contains(t, last.Outcome, audit.OutcomeFailure)
}

func TestVerifyClaimAndDeliver_Success(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", Username: "alice"}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		AuditLogger:    auditLog,
		Threshold:      2,
		factorChallenges: map[string]*CustodianFactorChallenge{
			"c1": {
				CustodianID: "c1",
				Nonce:       []byte("nonce"),
				ExpiresAt:   time.Now().Add(time.Minute),
			},
		},
	}
	svc := newCeremonyServiceWithExt(t, ext)

	c, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", &authfactors.Response{})
	require.NoError(t, err)
	assert.Equal(t, "alice", c.Username)

	require.NotEmpty(t, auditLog.events)
	assert.Equal(t, audit.OutcomeSuccess, auditLog.events[len(auditLog.events)-1].Outcome)
}

func TestVerifyClaimAndDeliver_Success_NoAuditLogger(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
		factorChallenges: map[string]*CustodianFactorChallenge{
			"c1": {CustodianID: "c1", Nonce: []byte("n"), ExpiresAt: time.Now().Add(time.Minute)},
		},
	}
	svc := newCeremonyServiceWithExt(t, ext)
	c, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", &authfactors.Response{})
	require.NoError(t, err)
	assert.Equal(t, "c1", c.ID)
}

func TestVerifyClaimAndDeliver_AlreadyDelivered_NoAuditLogger(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", ShareClaimed: true}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)
	_, err := svc.VerifyClaimAndDeliver(context.Background(), "c1", "ch1", nil)
	assert.ErrorIs(t, err, ErrShareAlreadyDelivered)
}

// ---------------------------------------------------------------------------
// SubmitUnsealShare
// ---------------------------------------------------------------------------

func TestSubmitUnsealShare_NilExtension(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{CustodianID: "c1"})
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestSubmitUnsealShare_CustodianNotFound(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		AuditLogger:    auditLog,
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{CustodianID: "missing"})
	assert.Error(t, err)
	require.NotEmpty(t, auditLog.events)
	assert.Contains(t, auditLog.events[len(auditLog.events)-1].Outcome, audit.OutcomeFailure)
}

func TestSubmitUnsealShare_RevokedCustodian(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	now := time.Now()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", RevokedAt: &now}

	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{CustodianID: "c1"})
	assert.ErrorIs(t, err, ErrCustodianRevoked)
}

func TestSubmitUnsealShare_FactorVerifyFails(t *testing.T) {
	verifyErr := errors.New("factor bad")
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{err: verifyErr},
		AuditLogger:    auditLog,
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID:    "c1",
		FactorResponse: &authfactors.Response{},
	})
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
	require.NotEmpty(t, auditLog.events)
	assert.Contains(t, auditLog.events[len(auditLog.events)-1].Outcome, audit.OutcomeFailure)
}

func TestSubmitUnsealShare_BelowThreshold(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		AuditLogger:    auditLog,
		Threshold:      3, // need 3, submitting only 1
	}
	svc := newCeremonyServiceWithExt(t, ext)

	result, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID:    "c1",
		FactorResponse: &authfactors.Response{},
		ShareBytes:     []byte("share-data"),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Submitted)
	assert.Equal(t, 3, result.Required)
	assert.False(t, result.Unsealed)
}

func TestSubmitUnsealShare_ThresholdMet_BarrierUnseals(t *testing.T) {
	// Build real Shamir shares so that reconstruct works end-to-end.
	shares, err := quicraftShamir.Split([]byte("root-key-bytes"), 2, 2)
	require.NoError(t, err)

	share0, err := json.Marshal(shares[0])
	require.NoError(t, err)
	share1, err := json.Marshal(shares[1])
	require.NoError(t, err)

	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	custSvc.custodians["c2"] = &custodian.Custodian{ID: "c2"}

	unsealer := &fakeBarrierUnsealer{}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		BarrierUnseal:  unsealer,
		AuditLogger:    auditLog,
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	// First custodian submits below threshold.
	result1, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID:    "c1",
		FactorResponse: &authfactors.Response{},
		ShareBytes:     share0,
	})
	require.NoError(t, err)
	assert.False(t, result1.Unsealed)

	// Second custodian crosses the threshold.
	result2, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID:    "c2",
		FactorResponse: &authfactors.Response{},
		ShareBytes:     share1,
	})
	require.NoError(t, err)
	assert.True(t, result2.Unsealed, "barrier must be unsealed once threshold is met")
	assert.True(t, unsealer.called, "UnsealWithKey must be called")
}

func TestSubmitUnsealShare_ThresholdMet_BarrierUnsealError(t *testing.T) {
	shares, err := quicraftShamir.Split([]byte("my-root-key"), 2, 2)
	require.NoError(t, err)

	s0, _ := json.Marshal(shares[0])
	s1, _ := json.Marshal(shares[1])

	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	custSvc.custodians["c2"] = &custodian.Custodian{ID: "c2"}

	unsealErr := errors.New("barrier init failed")
	unsealer := &fakeBarrierUnsealer{unsealErr: unsealErr}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		BarrierUnseal:  unsealer,
		AuditLogger:    auditLog,
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, _ = svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID: "c1", FactorResponse: &authfactors.Response{}, ShareBytes: s0,
	})

	_, err = svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID: "c2", FactorResponse: &authfactors.Response{}, ShareBytes: s1,
	})
	assert.ErrorIs(t, err, unsealErr)

	require.NotEmpty(t, auditLog.events)
	last := auditLog.events[len(auditLog.events)-1]
	assert.Contains(t, last.Outcome, audit.OutcomeFailure)
}

func TestSubmitUnsealShare_ThresholdMet_NilBarrierUnsealer(t *testing.T) {
	shares, err := quicraftShamir.Split([]byte("key"), 2, 2)
	require.NoError(t, err)
	s0, _ := json.Marshal(shares[0])
	s1, _ := json.Marshal(shares[1])

	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	custSvc.custodians["c2"] = &custodian.Custodian{ID: "c2"}

	// BarrierUnseal is nil; reconstructAndUnseal must return ErrNotImplemented.
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)

	_, _ = svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID: "c1", FactorResponse: &authfactors.Response{}, ShareBytes: s0,
	})
	_, err = svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID: "c2", FactorResponse: &authfactors.Response{}, ShareBytes: s1,
	})
	assert.ErrorIs(t, err, ErrNotImplemented)
}

func TestSubmitUnsealShare_FactorVerifyFails_NoAuditLogger(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1"}
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{err: errors.New("bad")},
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)
	_, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{
		CustodianID: "c1", FactorResponse: &authfactors.Response{},
	})
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestSubmitUnsealShare_CustodianNotFound_NoAuditLogger(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	ext := &CeremonyExtension{
		CustodianSvc:   custSvc,
		FactorVerifier: &fakeFactorVerifier{},
		Threshold:      2,
	}
	svc := newCeremonyServiceWithExt(t, ext)
	_, err := svc.SubmitUnsealShare(context.Background(), SubmitUnsealShareRequest{CustodianID: "nobody"})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// reconstructAndUnseal — invalid share JSON
// ---------------------------------------------------------------------------

func TestReconstructAndUnseal_InvalidShareJSON(t *testing.T) {
	unsealer := &fakeBarrierUnsealer{}
	ext := &CeremonyExtension{BarrierUnseal: unsealer, Threshold: 2}
	svc := newCeremonyServiceWithExt(t, ext)

	err := svc.reconstructAndUnseal(context.Background(), [][]byte{[]byte("not-json")}, ext)
	require.Error(t, err)
	var shamirErr *ShamirSplitError
	assert.ErrorAs(t, err, &shamirErr)
}

// ---------------------------------------------------------------------------
// unmarshalShare
// ---------------------------------------------------------------------------

func TestUnmarshalShare_ValidJSON(t *testing.T) {
	shares, err := quicraftShamir.Split([]byte("secret"), 2, 3)
	require.NoError(t, err)

	data, err := json.Marshal(shares[0])
	require.NoError(t, err)

	var sh quicraftShamir.Share
	err = unmarshalShare(data, &sh)
	require.NoError(t, err)
	assert.Equal(t, shares[0].Index, sh.Index)
}

func TestUnmarshalShare_InvalidJSON(t *testing.T) {
	err := unmarshalShare([]byte("{bad"), &quicraftShamir.Share{})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// sentinelError / Error()
// ---------------------------------------------------------------------------

func TestSentinelError_ErrorMethod(t *testing.T) {
	e := newSentinel("ceremony: test sentinel")
	assert.Equal(t, "ceremony: test sentinel", e.Error())
}

func TestErrShareAlreadyDelivered_IsDistinct(t *testing.T) {
	assert.NotErrorIs(t, ErrShareAlreadyDelivered, ErrCustodianRevoked)
	assert.ErrorIs(t, ErrShareAlreadyDelivered, ErrShareAlreadyDelivered)
}
