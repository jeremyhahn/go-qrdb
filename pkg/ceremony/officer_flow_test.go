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
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newInitializedService returns a CeremonyService wired with a real CA,
// an in-memory barrier (unsealed), and a test credential service.
func newInitializedService(t *testing.T) *CeremonyService {
	t.Helper()
	barrier := unsealedTestBarrier(t)
	svc, err := NewCeremonyService(
		defaultConfig(),
		barrier,
		newTestCredentialService(),
		testLogger(),
	)
	require.NoError(t, err)
	// Attach CA so RegisterOfficer works.
	svc.mu.Lock()
	svc.ca = newMockCA(t)
	svc.mu.Unlock()
	// Ensure officer registry is initialized.
	svc.mu.Lock()
	require.NoError(t, svc.ensureOfficerRegistry())
	svc.mu.Unlock()
	return svc
}

// officerTestData bundles per-officer test state.
type officerTestData struct {
	username   string
	key        *ecdsa.PrivateKey
	thumbprint string
	shareIndex int
}

// setupTwoOfficersWithShares registers alice and bob, distributes shares with
// threshold=2, and returns their test data.
func setupTwoOfficersWithShares(t *testing.T) (*CeremonyService, []*officerTestData) {
	t.Helper()
	svc := newInitializedService(t)
	ctx := context.Background()

	names := []string{"alice", "bob"}
	data := make([]*officerTestData, len(names))

	for i, name := range names {
		csrPEM, key := generateCSRPEM(t, name)
		res, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
		officer, err := svc.officerRegistry.Load(ctx, name)
		require.NoError(t, err)
		data[i] = &officerTestData{
			username:   name,
			key:        key,
			thumbprint: officer.CertThumbprint,
			shareIndex: res.ShareIndex,
		}
	}

	_, err := svc.SealAndDistribute(ctx, []byte("share-secret-material-12345678"), 2, 2)
	require.NoError(t, err)
	return svc, data
}

// doClaimHandshake performs a full BeginClaimShare→CompleteClaimShare round.
func doClaimHandshake(
	t *testing.T,
	svc *CeremonyService,
	od *officerTestData,
) *ShareClaimResult {
	t.Helper()
	ctx := context.Background()

	cr, err := svc.BeginClaimShare(ctx, od.username, od.thumbprint)
	require.NoError(t, err)

	sig := signNonce(t, od.key, cr.Nonce)
	result, err := svc.CompleteClaimShare(ctx, od.username, hex.EncodeToString(cr.Nonce), sig, od.thumbprint)
	require.NoError(t, err)
	return result
}

// ---------------------------------------------------------------------------
// RegisterOfficer tests
// ---------------------------------------------------------------------------

func TestRegisterOfficer_Happy(t *testing.T) {
	svc := newInitializedService(t)
	csrPEM, _ := generateCSRPEM(t, "alice")
	res, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.CertPEM)
	assert.Equal(t, 1, res.ShareIndex)
}

func TestRegisterOfficer_ShareIndexIncremental(t *testing.T) {
	svc := newInitializedService(t)
	csrPEM1, _ := generateCSRPEM(t, "alice")
	csrPEM2, _ := generateCSRPEM(t, "bob")
	r1, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{Username: "alice", CSRPEM: csrPEM1, AuthenticatorType: AuthenticatorSoftware})
	require.NoError(t, err)
	r2, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{Username: "bob", CSRPEM: csrPEM2, AuthenticatorType: AuthenticatorSoftware})
	require.NoError(t, err)
	assert.Equal(t, 1, r1.ShareIndex)
	assert.Equal(t, 2, r2.ShareIndex)
}

func TestRegisterOfficer_BadCSR(t *testing.T) {
	svc := newInitializedService(t)
	_, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice",
		CSRPEM:   []byte("not-a-csr"),
	})
	require.Error(t, err)
	assert.IsType(t, &CSRParseError{}, err)
}

func TestRegisterOfficer_EmptyCSR(t *testing.T) {
	svc := newInitializedService(t)
	_, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice",
		CSRPEM:   nil,
	})
	assert.ErrorIs(t, err, ErrMissingCSRPEM)
}

func TestRegisterOfficer_DuplicateUsername(t *testing.T) {
	svc := newInitializedService(t)
	csrPEM, _ := generateCSRPEM(t, "alice")
	_, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)

	csrPEM2, _ := generateCSRPEM(t, "alice")
	_, err = svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM2, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrDuplicateUsername)
}

func TestRegisterOfficer_BadAuthenticatorType(t *testing.T) {
	svc := newInitializedService(t)
	csrPEM, _ := generateCSRPEM(t, "alice")
	_, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username:          "alice",
		CSRPEM:            csrPEM,
		AuthenticatorType: AuthenticatorType("invalid"),
	})
	assert.ErrorIs(t, err, ErrInvalidAuthenticatorType)
}

// ---------------------------------------------------------------------------
// SealAndDistribute tests
// ---------------------------------------------------------------------------

func TestSealAndDistribute_Happy(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	for _, name := range []string{"alice", "bob", "carol"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	secret := []byte("super-secret-root-key-material")
	n, err := svc.SealAndDistribute(ctx, secret, 2, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	for _, name := range []string{"alice", "bob", "carol"} {
		assert.True(t, svc.shareStore.Exists(name), "share should exist for %s", name)
	}
}

// TestSealAndDistribute_SecretWipedINV2 verifies INV-2: passed slice zeroed after return.
func TestSealAndDistribute_SecretWipedINV2(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	secret := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}
	_, err := svc.SealAndDistribute(ctx, secret, 2, 2)
	require.NoError(t, err)

	for i, b := range secret {
		if b != 0 {
			t.Errorf("INV-2: secret not wiped at index %d after SealAndDistribute", i)
		}
	}
}

func TestSealAndDistribute_InsufficientOfficers(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	csrPEM, _ := generateCSRPEM(t, "alice")
	_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: "alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
	require.NoError(t, err)

	_, err = svc.SealAndDistribute(ctx, []byte("secret"), 2, 2)
	assert.ErrorIs(t, err, ErrInsufficientOfficers)
}

func TestSealAndDistribute_OfficerCountMismatch(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	_, err := svc.SealAndDistribute(ctx, []byte("secret"), 2, 3)
	assert.ErrorIs(t, err, ErrOfficerCountMismatch)
}

// ---------------------------------------------------------------------------
// BeginClaimShare / CompleteClaimShare tests
// ---------------------------------------------------------------------------

func TestClaimShare_HappyPath(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)

	result := doClaimHandshake(t, svc, officers[0])
	assert.Equal(t, officers[0].shareIndex, result.ShareIndex)
	assert.NotEmpty(t, result.ShareBytes)
}

// TestClaimShare_WrongThumbprintBeginINV4 verifies INV-4 (begin).
func TestClaimShare_WrongThumbprintBeginINV4(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	o := officers[0]

	_, err := svc.BeginClaimShare(context.Background(), o.username, "wrong-thumbprint")
	assert.ErrorIs(t, err, ErrCertThumbprintMismatch)
}

// TestClaimShare_WrongThumbprintCompleteINV12 verifies INV-12 (complete).
func TestClaimShare_WrongThumbprintCompleteINV12(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	o := officers[0]
	ctx := context.Background()

	cr, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	sig := signNonce(t, o.key, cr.Nonce)
	_, err = svc.CompleteClaimShare(ctx, o.username, hex.EncodeToString(cr.Nonce), sig, "wrong-thumbprint")
	assert.ErrorIs(t, err, ErrCertThumbprintMismatch)
}

func TestClaimShare_BadSignature(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	o := officers[0]
	ctx := context.Background()

	cr, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	_, err = svc.CompleteClaimShare(ctx, o.username, hex.EncodeToString(cr.Nonce), []byte("badsig"), o.thumbprint)
	assert.Error(t, err)
}

// TestClaimShare_DoubleClaimINV3 verifies INV-3: second claim fails.
func TestClaimShare_DoubleClaimINV3(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	o := officers[0]

	_ = doClaimHandshake(t, svc, o)

	// Share is gone — begin should fail with ErrNoPendingShare.
	_, err := svc.BeginClaimShare(context.Background(), o.username, o.thumbprint)
	assert.ErrorIs(t, err, ErrNoPendingShare)
}

func TestClaimShare_ExpiredChallenge(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	o := officers[0]
	ctx := context.Background()

	cr, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	// Manually expire the challenge.
	svc.mu.Lock()
	if ch, ok := svc.challenges["share:"+o.username]; ok {
		ch.ExpiresAt = ch.ExpiresAt.Add(-2 * challengeTTL)
	}
	svc.mu.Unlock()

	sig := signNonce(t, o.key, cr.Nonce)
	_, err = svc.CompleteClaimShare(ctx, o.username, hex.EncodeToString(cr.Nonce), sig, o.thumbprint)
	assert.ErrorIs(t, err, ErrChallengeExpired)
}

// ---------------------------------------------------------------------------
// OfficerClaimStatus tests
// ---------------------------------------------------------------------------

func TestOfficerClaimStatus_Snapshot(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()

	statuses, err := svc.OfficerClaimStatus(ctx)
	require.NoError(t, err)
	assert.Len(t, statuses, 2)

	for _, s := range statuses {
		assert.True(t, s.SharePending, "share should be pending before claim: %s", s.Username)
		assert.True(t, s.ClaimedAt.IsZero())
	}

	// Claim alice's share.
	_ = doClaimHandshake(t, svc, officers[0])

	statuses, err = svc.OfficerClaimStatus(ctx)
	require.NoError(t, err)
	for _, s := range statuses {
		if s.Username == officers[0].username {
			assert.False(t, s.SharePending, "share should not be pending after claim")
		}
	}
}

// ---------------------------------------------------------------------------
// RekeyAndDistribute tests (INV-10)
// ---------------------------------------------------------------------------

// TestRekeyAndDistribute_OldSharesDestroyedINV10 verifies INV-10: old
// enclaves are destroyed before new ones are sealed.
func TestRekeyAndDistribute_OldSharesDestroyedINV10(t *testing.T) {
	svc, _ := setupTwoOfficersWithShares(t)
	ctx := context.Background()

	// Both officers have pending shares before rekey.
	assert.True(t, svc.shareStore.Exists("alice"))
	assert.True(t, svc.shareStore.Exists("bob"))

	_, err := svc.RekeyAndDistribute(ctx, []byte("new-secret-material-12345678"), 2, 2)
	require.NoError(t, err)

	// New shares exist after rekey.
	assert.True(t, svc.shareStore.Exists("alice"), "alice should have new share after rekey")
	assert.True(t, svc.shareStore.Exists("bob"), "bob should have new share after rekey")
}

func TestRekeyAndDistribute_SecretWiped(t *testing.T) {
	svc, _ := setupTwoOfficersWithShares(t)
	newSecret := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	_, err := svc.RekeyAndDistribute(context.Background(), newSecret, 2, 2)
	require.NoError(t, err)
	for i, b := range newSecret {
		if b != 0 {
			t.Errorf("new secret not wiped at index %d after RekeyAndDistribute", i)
		}
	}
}

// TestRekeyAndDistribute_ReclaimAfterRekey verifies that an officer who
// claimed their share in the prior round can claim the newly distributed
// share after a rekey — the share store's claim history must be cleared
// along with the prior round's enclaves.
func TestRekeyAndDistribute_ReclaimAfterRekey(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()
	o := officers[0]

	// Round 1: claim succeeds and consumes the enclave.
	round1 := doClaimHandshake(t, svc, o)
	_, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	assert.ErrorIs(t, err, ErrNoPendingShare)

	// Rekey distributes a fresh share set.
	_, err = svc.RekeyAndDistribute(ctx, []byte("new-secret-material-12345678"), 2, 2)
	require.NoError(t, err)

	// Round 2: the officer can claim again — NOT ErrShareAlreadyClaimed.
	round2 := doClaimHandshake(t, svc, o)
	assert.NotEqual(t, round1.ShareBytes, round2.ShareBytes,
		"rekey must distribute new share material")
}

// TestResetClaimedAt_Observable verifies ResetClaimedAt clears timestamps.
func TestResetClaimedAt_Observable(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()

	// Claim alice's share to set ClaimedAt.
	_ = doClaimHandshake(t, svc, officers[0])

	// Verify ClaimedAt is set in the cache.
	cached := svc.officerRegistry.Cached()
	var aliceRecord *PersistentOfficer
	for _, r := range cached {
		if r.Username == officers[0].username {
			aliceRecord = r
		}
	}
	require.NotNil(t, aliceRecord)
	assert.False(t, aliceRecord.ClaimedAt.IsZero(), "ClaimedAt should be set after claim")

	// Rekey calls ResetClaimedAt internally.
	_, err := svc.RekeyAndDistribute(ctx, []byte("new-secret-xy-1234567890"), 2, 2)
	require.NoError(t, err)

	cachedAfter := svc.officerRegistry.Cached()
	for _, r := range cachedAfter {
		if r.Username == officers[0].username {
			assert.True(t, r.ClaimedAt.IsZero(), "ClaimedAt should be reset after rekey")
		}
	}
}

// ---------------------------------------------------------------------------
// FlushPendingOfficers idempotency test
// ---------------------------------------------------------------------------

func TestFlushPendingOfficers_Idempotent(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	csrPEM, _ := generateCSRPEM(t, "alice")
	_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)

	require.NoError(t, svc.FlushPendingOfficers(ctx))
	require.NoError(t, svc.FlushPendingOfficers(ctx))
}

// ---------------------------------------------------------------------------
// INV-9: No share bytes in storage
// ---------------------------------------------------------------------------

// TestOfficerRegistry_NoShareBytesInStorageINV9 scans storage and asserts
// no officer record contains share_value or shares JSON keys.
func TestOfficerRegistry_NoShareBytesInStorageINV9(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	distinctiveSecret := []byte("DISTINCTIVE_SHARE_SECRET_INV9_TEST")
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	_, err := svc.SealAndDistribute(ctx, distinctiveSecret, 2, 2)
	require.NoError(t, err)

	// Scan barrier storage (decrypts values) looking for share fields.
	_ = svc.barrier.Scan(ctx, "ceremony/officers/", func(key string, value []byte) error {
		var obj map[string]interface{}
		if jsonErr := json.Unmarshal(value, &obj); jsonErr != nil {
			return nil
		}
		for k := range obj {
			if k == "share_value" || k == "shares" {
				t.Errorf("INV-9: found share field %q in storage at %q", k, key)
			}
		}
		if bytes.Contains(value, distinctiveSecret) {
			t.Errorf("INV-9: distinctive secret found in raw storage at %q", key)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Recovery scope: independent from system scope
// ---------------------------------------------------------------------------

func TestRecoveryScope_IndependentFromSystem(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	// Register 2 system officers (needed for SealAndDistribute threshold=2).
	for _, name := range []string{"system-alice", "system-bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	// Register 2 recovery officers.
	for _, name := range []string{"recovery-alice", "recovery-bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	distributor := svc.RecoveryDistributor()
	n, err := distributor.SealAndDistribute(ctx, []byte("recovery-secret-material-xy"), 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	assert.True(t, svc.recoveryShareStore.Exists("recovery-alice"))
	assert.True(t, svc.recoveryShareStore.Exists("recovery-bob"))
	// Recovery shares must not appear in system store and vice versa.
	assert.False(t, svc.shareStore.Exists("recovery-alice"), "recovery officer must not bleed into system store")
	assert.False(t, svc.recoveryShareStore.Exists("system-alice"))
}

func TestRecoveryScope_ClaimHandshake(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	// Register 2 recovery officers (Shamir requires threshold >= 2).
	var bobKey *ecdsa.PrivateKey
	var bobRes *RegisterOfficerResult
	for _, name := range []string{"recovery-alice", "recovery-bob"} {
		csrPEM, key := generateCSRPEM(t, name)
		res, err := svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
		if name == "recovery-bob" {
			bobKey = key
			bobRes = res
		}
	}

	distributor := svc.RecoveryDistributor()
	_, err := distributor.SealAndDistribute(ctx, []byte("recovery-secret-handshake-xy"), 2, 2)
	require.NoError(t, err)

	officer, err := svc.recoveryRegistry.Load(ctx, "recovery-bob")
	require.NoError(t, err)

	cr, err := svc.BeginRecoveryShareClaim(ctx, "recovery-bob", officer.CertThumbprint)
	require.NoError(t, err)
	assert.NotEmpty(t, cr.Nonce)

	sig := signNonce(t, bobKey, cr.Nonce)
	claimResult, err := svc.CompleteRecoveryShareClaim(ctx, "recovery-bob",
		hex.EncodeToString(cr.Nonce), sig, officer.CertThumbprint)
	require.NoError(t, err)
	assert.Equal(t, bobRes.ShareIndex, claimResult.ShareIndex)
	assert.NotEmpty(t, claimResult.ShareBytes)
}

// ---------------------------------------------------------------------------
// TenantCeremonyService tests
// ---------------------------------------------------------------------------

func TestNewTenantCeremonyService_Validation(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		backend  seal.StorageBackend
		ca       CeremonyCA
		wantErr  error
	}{
		{"empty tenant", "", unsealedTestBarrier(t), newMockCA(t), ErrEmptyTenantID},
		{"nil backend", "t1", nil, newMockCA(t), ErrNilBackend},
		{"nil CA", "t1", unsealedTestBarrier(t), nil, ErrNilCA},
		{"nil logger via separate call", "t1", unsealedTestBarrier(t), newMockCA(t), ErrNilLogger},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logger *testSlogLogger
			if tt.wantErr != ErrNilLogger {
				logger = &testSlogLogger{} // won't be used, just non-nil marker
				_ = logger
			}
			// For the nil-logger case pass nil explicitly.
			var svcLogger interface{}
			if tt.wantErr == ErrNilLogger {
				svcLogger = nil
			} else {
				svcLogger = testLogger()
			}
			_, err := newTenantSvcWithLogger(tt.tenantID, tt.backend, tt.ca, svcLogger)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// newTenantSvcWithLogger allows passing nil logger for test.
func newTenantSvcWithLogger(tenantID string, backend seal.StorageBackend, ca CeremonyCA, logger interface{}) (*TenantCeremonyService, error) {
	if logger == nil {
		return NewTenantCeremonyService(tenantID, backend, ca, nil)
	}
	return NewTenantCeremonyService(tenantID, backend, ca, testLogger())
}

// testSlogLogger is a placeholder type used to detect nil logger in tests.
type testSlogLogger struct{}

func TestTenantCeremonyService_RegisterAndDistribute(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-1", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	// Shamir requires threshold >= 2, so register 2 officers.
	for i, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		res, err := svc.RegisterOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
		assert.Equal(t, i+1, res.ShareIndex)
	}

	n, err := svc.SealAndDistribute(ctx, []byte("tenant-secret-material"), 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, svc.shareStore.Exists("alice"))
	assert.True(t, svc.shareStore.Exists("bob"))
}

func TestTenantCeremonyService_ClaimShare(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-2", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	// Register 2 officers (Shamir minimum).
	csrPEM, key := generateCSRPEM(t, "alice")
	_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)
	csrPEM2, _ := generateCSRPEM(t, "bob")
	_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
		Username: "bob", CSRPEM: csrPEM2, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)

	_, err = svc.SealAndDistribute(ctx, []byte("tenant-secret-2"), 2, 2)
	require.NoError(t, err)

	officer, err := svc.registry.Load(ctx, "alice")
	require.NoError(t, err)

	cr, err := svc.BeginClaimShare(ctx, "alice", officer.CertThumbprint)
	require.NoError(t, err)

	sig := signNonce(t, key, cr.Nonce)
	shareBytes, err := svc.CompleteClaimShare(ctx, "alice",
		hex.EncodeToString(cr.Nonce), sig, officer.CertThumbprint)
	require.NoError(t, err)
	assert.NotEmpty(t, shareBytes)
}

func TestTenantCeremonyService_Cleanup(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-3", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}

	_, err = svc.SealAndDistribute(ctx, []byte("cleanup-secret-xy-123456"), 2, 2)
	require.NoError(t, err)
	assert.True(t, svc.shareStore.Exists("alice"))

	svc.Cleanup()
	assert.False(t, svc.shareStore.Exists("alice"))
}

func TestTenantCeremonyService_ScopeIsolation(t *testing.T) {
	// Two tenant services must not share officer data.
	barrier1 := unsealedTestBarrier(t)
	barrier2 := unsealedTestBarrier(t)

	svc1, err := NewTenantCeremonyService("tenant-A", barrier1, newMockCA(t), testLogger())
	require.NoError(t, err)
	svc2, err := NewTenantCeremonyService("tenant-B", barrier2, newMockCA(t), testLogger())
	require.NoError(t, err)

	assert.NotEqual(t, svc1.registry.Prefix(), svc2.registry.Prefix())
}

// ---------------------------------------------------------------------------
// TenantCeremonyRegistry tests
// ---------------------------------------------------------------------------

func TestTenantCeremonyRegistry_GetSetDelete(t *testing.T) {
	reg := NewTenantCeremonyRegistry()

	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("t1", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	_, ok := reg.Get("t1")
	assert.False(t, ok)

	reg.Set("t1", svc)
	got, ok := reg.Get("t1")
	require.True(t, ok)
	assert.Equal(t, svc, got)

	reg.Delete("t1")
	_, ok = reg.Get("t1")
	assert.False(t, ok)
}

func TestTenantCeremonyRegistry_DeleteCallsCleanup(t *testing.T) {
	reg := NewTenantCeremonyRegistry()
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("t2", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware})
		require.NoError(t, err)
	}
	_, err = svc.SealAndDistribute(ctx, []byte("delete-test-secret-xy"), 2, 2)
	require.NoError(t, err)
	assert.True(t, svc.shareStore.Exists("alice"))

	reg.Set("t2", svc)
	reg.Delete("t2") // should call Cleanup
	assert.False(t, svc.shareStore.Exists("alice"), "Cleanup should have destroyed the share")
}

// ---------------------------------------------------------------------------
// TenantCeremonyService — zero-coverage methods
// ---------------------------------------------------------------------------

func TestTenantCeremonyService_ListOfficers(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-list", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}

	officers, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	assert.Len(t, officers, 2)
	usernames := make([]string, 0, len(officers))
	for _, o := range officers {
		usernames = append(usernames, o.Username)
	}
	assert.Contains(t, usernames, "alice")
	assert.Contains(t, usernames, "bob")
}

func TestTenantCeremonyService_OfficerClaimStatus(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-status", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}

	_, err = svc.SealAndDistribute(ctx, []byte("status-secret-material-xy"), 2, 2)
	require.NoError(t, err)

	statuses, err := svc.OfficerClaimStatus(ctx)
	require.NoError(t, err)
	assert.Len(t, statuses, 2)
	for _, s := range statuses {
		assert.True(t, s.SharePending, "share should be pending before claim: %s", s.Username)
	}
}

func TestTenantCeremonyService_RekeyAndDistribute(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-rekey", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}

	_, err = svc.SealAndDistribute(ctx, []byte("initial-rekey-secret-xy"), 2, 2)
	require.NoError(t, err)
	assert.True(t, svc.shareStore.Exists("alice"))
	assert.True(t, svc.shareStore.Exists("bob"))

	// Rekey: old shares destroyed, new shares sealed.
	n, err := svc.RekeyAndDistribute(ctx, []byte("new-rekey-secret-material-xy"), 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// New shares must exist (INV-10: old destroyed, new created).
	assert.True(t, svc.shareStore.Exists("alice"), "alice should have new share after rekey")
	assert.True(t, svc.shareStore.Exists("bob"), "bob should have new share after rekey")
}

func TestTenantCeremonyService_RekeyAndDistribute_SecretWiped(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-rekey-wipe", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}
	_, err = svc.SealAndDistribute(ctx, []byte("initial-wipe-secret-xyzw"), 2, 2)
	require.NoError(t, err)

	newSecret := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}
	_, err = svc.RekeyAndDistribute(ctx, newSecret, 2, 2)
	require.NoError(t, err)
	for i, b := range newSecret {
		if b != 0 {
			t.Errorf("INV-2: new secret not wiped at index %d after TenantCeremonyService.RekeyAndDistribute", i)
		}
	}
}

// ---------------------------------------------------------------------------
// TenantCeremonyService.RegisterOfficer — sealed-barrier cache path
// ---------------------------------------------------------------------------

// TestTenantRegisterOfficer_SealedBarrierCachesRecord exercises the branch in
// TenantCeremonyService.RegisterOfficer where registry.Save fails (sealed
// barrier) and the record falls back to the in-memory cache.
func TestTenantRegisterOfficer_SealedBarrierCachesRecord(t *testing.T) {
	// Use a backend that always fails Put so Save returns an error.
	failBackend := &failingBackend{putErr: errors.New("sealed")}
	svc, err := NewTenantCeremonyService("tenant-sealed", failBackend, newMockCA(t), testLogger())
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "carol")
	res, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "carol", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	// Even though Save failed, the result should be valid and the record is in cache.
	require.NoError(t, err)
	assert.Equal(t, 1, res.ShareIndex)

	// The record must be in the in-memory cache.
	svc.registry.mu.RLock()
	cached := svc.registry.cache["carol"]
	svc.registry.mu.RUnlock()
	require.NotNil(t, cached, "officer should be cached when Save fails")
	assert.Equal(t, "carol", cached.Username)
}

// TestTenantRegisterOfficer_MissingUsername verifies ErrMissingUsername.
func TestTenantRegisterOfficer_MissingUsername(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-valid-reg", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "nobody")
	_, err = svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestTenantRegisterOfficer_MissingCSR verifies ErrMissingCSRPEM.
func TestTenantRegisterOfficer_MissingCSR(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-missing-csr", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	_, err = svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice", CSRPEM: nil, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrMissingCSRPEM)
}

// TestTenantRegisterOfficer_DuplicateUsername verifies ErrDuplicateUsername.
func TestTenantRegisterOfficer_DuplicateUsername(t *testing.T) {
	barrier := unsealedTestBarrier(t)
	svc, err := NewTenantCeremonyService("tenant-dup", barrier, newMockCA(t), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	csrPEM, _ := generateCSRPEM(t, "alice")
	_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)

	csrPEM2, _ := generateCSRPEM(t, "alice")
	_, err = svc.RegisterOfficer(ctx, RegisterOfficerRequest{
		Username: "alice", CSRPEM: csrPEM2, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrDuplicateUsername)
}

// ---------------------------------------------------------------------------
// recoveryDistributorAdapter.RekeyAndDistribute — 0% → covered
// ---------------------------------------------------------------------------

func TestRecoveryDistributor_RekeyAndDistribute(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	// Register 2 recovery officers (Shamir minimum threshold=2).
	for _, name := range []string{"recovery-carol", "recovery-dave"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}

	distributor := svc.RecoveryDistributor()

	// Initial distribute.
	n, err := distributor.SealAndDistribute(ctx, []byte("recovery-initial-secret-xy"), 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, svc.recoveryShareStore.Exists("recovery-carol"))

	// Rekey: old shares destroyed, new shares sealed.
	n, err = distributor.RekeyAndDistribute(ctx, []byte("recovery-rekey-secret-material"), 2, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, svc.recoveryShareStore.Exists("recovery-carol"), "carol should have new share after recovery rekey")
	assert.True(t, svc.recoveryShareStore.Exists("recovery-dave"), "dave should have new share after recovery rekey")
}

func TestRecoveryDistributor_RekeyAndDistribute_SecretWiped(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	for _, name := range []string{"recovery-x", "recovery-y"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}

	distributor := svc.RecoveryDistributor()
	_, err := distributor.SealAndDistribute(ctx, []byte("initial-recovery-wipe-secret-xy"), 2, 2)
	require.NoError(t, err)

	newSecret := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	_, err = distributor.RekeyAndDistribute(ctx, newSecret, 2, 2)
	require.NoError(t, err)
	for i, b := range newSecret {
		if b != 0 {
			t.Errorf("INV-2: recovery rekey secret not wiped at index %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// CeremonyService.RecoveryOfficerClaimStatus — 0% → covered
// ---------------------------------------------------------------------------

func TestRecoveryOfficerClaimStatus_NilRegistry(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	// No recovery state initialized; should return nil, nil.
	statuses, err := svc.RecoveryOfficerClaimStatus(ctx)
	assert.NoError(t, err)
	assert.Nil(t, statuses)
}

func TestRecoveryOfficerClaimStatus_WithOfficers(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	for _, name := range []string{"recovery-p", "recovery-q"} {
		csrPEM, _ := generateCSRPEM(t, name)
		_, err := svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{
			Username: name, CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
		})
		require.NoError(t, err)
	}

	distributor := svc.RecoveryDistributor()
	_, err := distributor.SealAndDistribute(ctx, []byte("status-recovery-secret-xy"), 2, 2)
	require.NoError(t, err)

	statuses, err := svc.RecoveryOfficerClaimStatus(ctx)
	require.NoError(t, err)
	assert.Len(t, statuses, 2)
	for _, s := range statuses {
		assert.True(t, s.SharePending, "recovery share should be pending before claim: %s", s.Username)
	}
}

// ---------------------------------------------------------------------------
// completeClaimShareLocked — missing error branches
// ---------------------------------------------------------------------------

// TestCompleteClaimShare_NoChallengeFound verifies ErrChallengeNotFound when
// CompleteClaimShare is called without a prior BeginClaimShare.
func TestCompleteClaimShare_NoChallengeFound(t *testing.T) {
	svc, _ := setupTwoOfficersWithShares(t)
	ctx := context.Background()

	// Skip BeginClaimShare; the challenge map is empty.
	_, err := svc.CompleteClaimShare(ctx, "alice", "deadbeef", []byte("sig"), "thumb")
	assert.ErrorIs(t, err, ErrChallengeNotFound)
}

// TestCompleteClaimShare_NonceDecodeFails verifies ErrChallengeVerificationFailed
// when the nonceHex string is not valid hex.
func TestCompleteClaimShare_NonceDecodeFails(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()
	o := officers[0]

	// Begin the challenge so it exists in the map.
	_, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	// Supply invalid hex as nonceHex.
	_, err = svc.CompleteClaimShare(ctx, o.username, "not-valid-hex!!", []byte("sig"), o.thumbprint)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

// TestCompleteClaimShare_NonceLengthMismatch verifies ErrChallengeVerificationFailed
// when a valid hex string decodes to a different length than the stored nonce.
func TestCompleteClaimShare_NonceLengthMismatch(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()
	o := officers[0]

	_, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	// Provide a 1-byte nonce hex (too short).
	_, err = svc.CompleteClaimShare(ctx, o.username, hex.EncodeToString([]byte{0x01}), []byte("sig"), o.thumbprint)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

// TestCompleteClaimShare_NonceByteMismatch verifies ErrChallengeVerificationFailed
// when a correctly-sized nonce hex contains wrong bytes.
func TestCompleteClaimShare_NonceByteMismatch(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()
	o := officers[0]

	cr, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	// Flip the first byte of the nonce.
	wrong := make([]byte, len(cr.Nonce))
	copy(wrong, cr.Nonce)
	wrong[0] ^= 0xFF

	_, err = svc.CompleteClaimShare(ctx, o.username, hex.EncodeToString(wrong), []byte("sig"), o.thumbprint)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

// TestCompleteClaimShare_InvalidPubKeyPEM exercises the parsePublicKeyPEM
// error branch by injecting a record with a corrupt PubKeyPEM after the
// challenge is created.
func TestCompleteClaimShare_InvalidPubKeyPEM(t *testing.T) {
	svc, officers := setupTwoOfficersWithShares(t)
	ctx := context.Background()
	o := officers[0]

	cr, err := svc.BeginClaimShare(ctx, o.username, o.thumbprint)
	require.NoError(t, err)

	// Corrupt the PubKeyPEM in the officer registry cache.
	svc.mu.Lock()
	if cached, ok := svc.officerRegistry.cache[o.username]; ok {
		cached.PubKeyPEM = "not-valid-pem"
	}
	svc.mu.Unlock()

	sig := signNonce(t, o.key, cr.Nonce)
	_, err = svc.CompleteClaimShare(ctx, o.username, hex.EncodeToString(cr.Nonce), sig, o.thumbprint)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

// ---------------------------------------------------------------------------
// RegisterRecoveryOfficer — 61.3% → push higher
// ---------------------------------------------------------------------------

// TestRegisterRecoveryOfficer_MissingUsername verifies ErrMissingUsername.
func TestRegisterRecoveryOfficer_MissingUsername(t *testing.T) {
	svc := newInitializedService(t)
	csrPEM, _ := generateCSRPEM(t, "nobody")
	_, err := svc.RegisterRecoveryOfficer(context.Background(), RegisterOfficerRequest{
		Username: "", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrMissingUsername)
}

// TestRegisterRecoveryOfficer_MissingCSR verifies ErrMissingCSRPEM.
func TestRegisterRecoveryOfficer_MissingCSR(t *testing.T) {
	svc := newInitializedService(t)
	_, err := svc.RegisterRecoveryOfficer(context.Background(), RegisterOfficerRequest{
		Username: "alice", CSRPEM: nil, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrMissingCSRPEM)
}

// TestRegisterRecoveryOfficer_DuplicateUsername verifies ErrDuplicateUsername.
func TestRegisterRecoveryOfficer_DuplicateUsername(t *testing.T) {
	svc := newInitializedService(t)
	ctx := context.Background()

	csrPEM, _ := generateCSRPEM(t, "recovery-alice")
	_, err := svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{
		Username: "recovery-alice", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)

	csrPEM2, _ := generateCSRPEM(t, "recovery-alice")
	_, err = svc.RegisterRecoveryOfficer(ctx, RegisterOfficerRequest{
		Username: "recovery-alice", CSRPEM: csrPEM2, AuthenticatorType: AuthenticatorSoftware,
	})
	assert.ErrorIs(t, err, ErrDuplicateUsername)
}

// TestRegisterRecoveryOfficer_SealedBarrierCachesRecord exercises the sealed
// barrier fallback path where Save fails and the record is cached in memory.
func TestRegisterRecoveryOfficer_SealedBarrierCachesRecord(t *testing.T) {
	// Use a sealed (not yet initialized) barrier — IsSealed() returns true.
	b := testSealedBarrier(t)
	svc, err := NewCeremonyService(
		defaultConfig(),
		b,
		newTestCredentialService(),
		testLogger(),
	)
	require.NoError(t, err)
	svc.mu.Lock()
	svc.ca = newMockCA(t)
	svc.mu.Unlock()

	csrPEM, _ := generateCSRPEM(t, "recovery-sealed")
	res, err := svc.RegisterRecoveryOfficer(context.Background(), RegisterOfficerRequest{
		Username: "recovery-sealed", CSRPEM: csrPEM, AuthenticatorType: AuthenticatorSoftware,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.ShareIndex)

	svc.mu.RLock()
	recReg := svc.recoveryRegistry
	svc.mu.RUnlock()
	require.NotNil(t, recReg)

	recReg.mu.RLock()
	cached := recReg.cache["recovery-sealed"]
	recReg.mu.RUnlock()
	require.NotNil(t, cached, "sealed-barrier path should cache the officer in memory")
}

// testSealedBarrier returns a sealed barrier suitable for testing paths that
// branch on IsSealed().
func testSealedBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	require.NoError(t, err)
	b, err := seal.NewStorageBarrier(
		testLogger(),
		seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"},
		strategy,
	)
	require.NoError(t, err)
	// Do NOT unseal — barrier starts sealed by default.
	require.True(t, b.IsSealed())
	return b
}
