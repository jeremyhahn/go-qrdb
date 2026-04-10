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
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// initializeMofN — SignCSR error branch
// ---------------------------------------------------------------------------

// TestInitialize_MofN_SignCSRFails_Coverage exercises the branch in initializeMofN
// where SignCSR returns an error for one of the officers.
func TestInitialize_MofN_SignCSRFails_Coverage(t *testing.T) {
	cfg, _ := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ca.failSignCSR = true

	_, err = svc.Initialize(context.Background(), ca)
	require.Error(t, err)
	var certErr *CertEncodeError
	assert.ErrorAs(t, err, &certErr)
}

// ---------------------------------------------------------------------------
// allClaimsComplete — false branch when share not yet claimed
// ---------------------------------------------------------------------------

// TestAllClaimsComplete_ShareNotClaimed exercises the branch in
// allClaimsComplete where the cert is claimed but the share has not been
// collected yet.
func TestAllClaimsComplete_ShareNotClaimed(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	_, err = svc.Initialize(context.Background(), newMockCA(t))
	require.NoError(t, err)

	ctx := context.Background()

	// Claim the cert for officer-0 (sets Claimed = true on the pending cert).
	challenge, err := svc.BeginClaimCert(ctx, "officer-0")
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	_, err = svc.CompleteClaimCert(ctx, "officer-0", hex.EncodeToString(challenge.Nonce), sig)
	require.NoError(t, err)

	// Calling ClaimShare after cert claim transitions the state only when
	// all officers are done. Since officer-1 and officer-2 haven't claimed,
	// allClaimsComplete returns false here. We confirm the state is still Enrolling.
	shareJSON, err := svc.ClaimShare(ctx, "officer-0")
	require.NoError(t, err)
	require.NotEmpty(t, shareJSON)

	// State should still be enrolling because others haven't claimed.
	assert.Equal(t, StateEnrolling, svc.State())
}

// ---------------------------------------------------------------------------
// BeginClaimCert — already claimed branch
// ---------------------------------------------------------------------------

// TestBeginClaimCert_AlreadyClaimed exercises the branch where the officer
// has already claimed their certificate.
func TestBeginClaimCert_AlreadyClaimed(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.Initialize(ctx, newMockCA(t))
	require.NoError(t, err)

	// First claim.
	challenge, err := svc.BeginClaimCert(ctx, "officer-0")
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	_, err = svc.CompleteClaimCert(ctx, "officer-0", hex.EncodeToString(challenge.Nonce), sig)
	require.NoError(t, err)

	// BeginClaimCert again after claiming should return ErrCertAlreadyClaimed.
	_, err = svc.BeginClaimCert(ctx, "officer-0")
	assert.ErrorIs(t, err, ErrCertAlreadyClaimed)
}

// ---------------------------------------------------------------------------
// CompleteClaimCert — CABundle error branch
// ---------------------------------------------------------------------------

// TestCompleteClaimCert_CABundleError_Coverage exercises the branch in
// CompleteClaimCert where ca.CABundle() returns an error.
func TestCompleteClaimCert_CABundleError_Coverage(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)

	ca := newErrorCA(t)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.Initialize(ctx, ca)
	require.NoError(t, err)

	challenge, err := svc.BeginClaimCert(ctx, "officer-0")
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	nonceHex := hex.EncodeToString(challenge.Nonce)

	// Now make CABundle fail.
	ca.failCABundle = true

	_, err = svc.CompleteClaimCert(ctx, "officer-0", nonceHex, sig)
	require.Error(t, err)
	var bundleErr *CABundleError
	assert.ErrorAs(t, err, &bundleErr)
}

// ---------------------------------------------------------------------------
// CompleteClaimCert — already claimed in pending cert
// ---------------------------------------------------------------------------

// TestCompleteClaimCert_AlreadyClaimed_Coverage exercises the branch where the pending
// cert is already marked as claimed.
func TestCompleteClaimCert_AlreadyClaimed_Coverage(t *testing.T) {
	cfg, keys := mofnConfig(t, 2, 3)
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.Initialize(ctx, newMockCA(t))
	require.NoError(t, err)

	// First complete claim succeeds.
	challenge, err := svc.BeginClaimCert(ctx, "officer-0")
	require.NoError(t, err)

	sig := signNonce(t, keys[0], challenge.Nonce)
	nHex := hex.EncodeToString(challenge.Nonce)

	_, err = svc.CompleteClaimCert(ctx, "officer-0", nHex, sig)
	require.NoError(t, err)

	// Manually inject a new challenge so CompleteClaimCert can proceed past
	// the challenge check but hit the already-claimed guard.
	svc.mu.Lock()
	newChallenge, _ := hex.DecodeString(hex.EncodeToString(challenge.Nonce))
	svc.challenges["officer-0"] = &ChallengeResponse{
		Nonce:     newChallenge,
		Username:  "officer-0",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	svc.mu.Unlock()

	// Now try to complete again — should hit ErrCertAlreadyClaimed.
	sig2 := signNonce(t, keys[0], challenge.Nonce)
	_, err = svc.CompleteClaimCert(ctx, "officer-0", hex.EncodeToString(challenge.Nonce), sig2)
	assert.ErrorIs(t, err, ErrCertAlreadyClaimed)
}

// ---------------------------------------------------------------------------
// SignCSRInit — SignCSR error branch
// ---------------------------------------------------------------------------

// TestSignCSRInit_SignCSRFails exercises the branch where ca.SignCSR fails
// during SignCSRInit.
func TestSignCSRInit_SignCSRFails(t *testing.T) {
	cfg := defaultConfig()
	svc, err := NewCeremonyService(cfg, testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)

	ca := newErrorCA(t)
	ctx := context.Background()
	_, err = svc.Initialize(ctx, ca)
	require.NoError(t, err)

	// Now make SignCSR fail.
	ca.failSignCSR = true

	csrPEM, _ := generateCSRPEM(t, "test-user")
	_, err = svc.SignCSRInit(ctx, "test-user", cfg.SOPin, csrPEM, "operator")
	assert.ErrorIs(t, err, ErrCSRSigningFailed)
}

// ---------------------------------------------------------------------------
// CustodianGroupService — CreateGroup store error
// ---------------------------------------------------------------------------

// TestCustodianGroupService_CreateGroup_StoreError exercises the branch in
// CreateGroup where putGroup (the store.Put call) returns an error.
func TestCustodianGroupService_CreateGroup_StoreError(t *testing.T) {
	backend := &errorableBackend{inner: seal.NewMemoryBackend(), failPut: true}
	svc, err := NewCustodianGroupService(backend, testLogger())
	require.NoError(t, err)

	_, err = svc.CreateGroup(context.Background(), "g1", "t1", "name", "purpose", 2, 3)
	require.Error(t, err)
	var gse *GroupStoreError
	assert.ErrorAs(t, err, &gse)
	assert.Equal(t, "put", gse.Op)
}

// ---------------------------------------------------------------------------
// CustodianGroupService — ListGroups getGroup error (unmarshal corrupt data)
// ---------------------------------------------------------------------------

// TestCustodianGroupService_ListGroups_CorruptData exercises the error path
// in ListGroups where getGroup encounters corrupt JSON data.
func TestCustodianGroupService_ListGroups_CorruptData(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, err := NewCustodianGroupService(store, testLogger())
	require.NoError(t, err)
	ctx := context.Background()

	// Inject a key with corrupt JSON directly into the underlying store.
	key := custodianGroupPrefix + "corrupt-group"
	require.NoError(t, store.Put(ctx, key, []byte("not-valid-json")))

	_, err = svc.ListGroups(ctx)
	require.Error(t, err)
	var gse *GroupStoreError
	assert.ErrorAs(t, err, &gse)
	assert.Equal(t, "unmarshal", gse.Op)
}

// ---------------------------------------------------------------------------
// CustodianGroupService — DeleteGroup exists-check error
// ---------------------------------------------------------------------------

// TestCustodianGroupService_DeleteGroup_ExistsError exercises the branch in
// DeleteGroup where store.Exists returns an error.
func TestCustodianGroupService_DeleteGroup_ExistsError(t *testing.T) {
	backend := &errorableBackend{inner: seal.NewMemoryBackend(), failExists: true}
	svc, err := NewCustodianGroupService(backend, testLogger())
	require.NoError(t, err)

	err = svc.DeleteGroup(context.Background(), "g1")
	require.Error(t, err)
	var gse *GroupStoreError
	assert.ErrorAs(t, err, &gse)
	assert.Equal(t, "exists-check", gse.Op)
}

// ---------------------------------------------------------------------------
// ShareService — SubmitShare put error
// ---------------------------------------------------------------------------

// TestShareService_SubmitShare_PutError exercises the branch in SubmitShare
// where store.Put returns an error.
func TestShareService_SubmitShare_PutError(t *testing.T) {
	backend := &errorableBackend{inner: seal.NewMemoryBackend(), failPut: true}
	svc, err := NewShareService(backend, nil, testLogger())
	require.NoError(t, err)

	_, err = svc.SubmitShare(context.Background(), &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("share-data"),
	})
	require.Error(t, err)
	var sse *ShareServiceError
	assert.ErrorAs(t, err, &sse)
	assert.Equal(t, "put", sse.Op)
}

// ---------------------------------------------------------------------------
// ShareService — ListShares scan error
// ---------------------------------------------------------------------------

// TestShareService_ListShares_ScanError exercises the branch in ListShares
// where the store.Scan call returns an error.
func TestShareService_ListShares_ScanError(t *testing.T) {
	backend := &errorableBackend{inner: seal.NewMemoryBackend(), failScan: true}
	svc, err := NewShareService(backend, nil, testLogger())
	require.NoError(t, err)

	_, err = svc.ListShares(context.Background())
	require.Error(t, err)
}

// TestShareService_ListShares_UnmarshalError exercises the branch in
// ListShares where an entry in the store contains corrupt JSON data.
func TestShareService_ListShares_UnmarshalError(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, err := NewShareService(store, nil, testLogger())
	require.NoError(t, err)
	ctx := context.Background()

	// Inject corrupt JSON at a share key so Scan invokes fn with bad data.
	key := sharePrefix + "g1/0"
	require.NoError(t, store.Put(ctx, key, []byte("not-valid-json")))

	_, err = svc.ListShares(ctx)
	require.Error(t, err)
	var sse *ShareServiceError
	assert.ErrorAs(t, err, &sse)
	assert.Equal(t, "unmarshal", sse.Op)
}

// ---------------------------------------------------------------------------
// ShareService — GetShareCollectionStatus list error
// ---------------------------------------------------------------------------

// TestShareService_GetShareCollectionStatus_ListError exercises the branch
// in GetShareCollectionStatus where store.List returns an error.
func TestShareService_GetShareCollectionStatus_ListError(t *testing.T) {
	backend := &errorableBackend{inner: seal.NewMemoryBackend(), failList: true}
	svc, err := NewShareService(backend, nil, testLogger())
	require.NoError(t, err)

	_, err = svc.GetShareCollectionStatus(context.Background(), "g1")
	require.Error(t, err)
	var sse *ShareServiceError
	assert.ErrorAs(t, err, &sse)
	assert.Equal(t, "list", sse.Op)
}

// ---------------------------------------------------------------------------
// CustodianGroupService — AddMember exists-check error
// ---------------------------------------------------------------------------

// TestCustodianGroupService_AddMember_ExistsCheckErrorInPut exercises the
// branch where putGroup fails when adding a member.
func TestCustodianGroupService_AddMember_PutError(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, err := NewCustodianGroupService(store, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	// Replace the store with one that fails on Put so AddMember's putGroup call
	// fails after the member is appended to the in-memory group struct.
	failStore := &errorableBackend{inner: store, failPut: true}
	svc.store = failStore

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.Error(t, err)
	var gse *GroupStoreError
	assert.ErrorAs(t, err, &gse)
	assert.Equal(t, "put", gse.Op)
}

// ---------------------------------------------------------------------------
// CustodianGroupService — RemoveMember put error
// ---------------------------------------------------------------------------

// TestCustodianGroupService_RemoveMember_PutError exercises the branch where
// putGroup fails after removing a member from the in-memory group.
func TestCustodianGroupService_RemoveMember_PutError(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, err := NewCustodianGroupService(store, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)

	// Replace store with a failing one.
	svc.store = &errorableBackend{inner: store, failPut: true}

	err = svc.RemoveMember(ctx, "g1", "user-1")
	require.Error(t, err)
	var gse *GroupStoreError
	assert.ErrorAs(t, err, &gse)
	assert.Equal(t, "put", gse.Op)
}

// ---------------------------------------------------------------------------
// CustodianGroupService — DistributeShares put error
// ---------------------------------------------------------------------------

// TestCustodianGroupService_DistributeShares_PutError exercises the branch
// where putGroup fails during DistributeShares after marking distributions.
func TestCustodianGroupService_DistributeShares_PutError(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, err := NewCustodianGroupService(store, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	_, err = svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "u1", "alice", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "u2", "bob", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "u3", "charlie", "email")
	require.NoError(t, err)

	// Now fail on Put.
	svc.store = &errorableBackend{inner: store, failPut: true}

	_, err = svc.DistributeShares(ctx, "g1")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// errorableBackend — a seal.StorageBackend that injects errors selectively
// ---------------------------------------------------------------------------

// errorableBackend wraps a real MemoryBackend and can inject errors on
// selected operations.
type errorableBackend struct {
	inner      seal.StorageBackend
	failGet    bool
	failPut    bool
	failDelete bool
	failList   bool
	failExists bool
	failScan   bool
}

var errInjectedBackend = errors.New("injected backend error")

func (b *errorableBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if b.failGet {
		return nil, errInjectedBackend
	}
	return b.inner.Get(ctx, key)
}

func (b *errorableBackend) Put(ctx context.Context, key string, data []byte) error {
	if b.failPut {
		return errInjectedBackend
	}
	return b.inner.Put(ctx, key, data)
}

func (b *errorableBackend) Delete(ctx context.Context, key string) error {
	if b.failDelete {
		return errInjectedBackend
	}
	return b.inner.Delete(ctx, key)
}

func (b *errorableBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if b.failList {
		return nil, errInjectedBackend
	}
	return b.inner.List(ctx, prefix)
}

func (b *errorableBackend) Exists(ctx context.Context, key string) (bool, error) {
	if b.failExists {
		return false, errInjectedBackend
	}
	return b.inner.Exists(ctx, key)
}

func (b *errorableBackend) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if b.failScan {
		return errInjectedBackend
	}
	return b.inner.Scan(ctx, prefix, fn)
}

func (b *errorableBackend) Close() error {
	return b.inner.Close()
}
