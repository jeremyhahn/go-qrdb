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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewShareService(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, err := NewShareService(store, nil, testLogger())
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewShareService_NilStore(t *testing.T) {
	_, err := NewShareService(nil, nil, testLogger())
	assert.ErrorIs(t, err, ErrNilStore)
}

func TestNewShareService_NilLogger(t *testing.T) {
	_, err := NewShareService(seal.NewMemoryBackend(), nil, nil)
	assert.ErrorIs(t, err, ErrNilLogger)
}

// ---------------------------------------------------------------------------
// SubmitShare tests
// ---------------------------------------------------------------------------

func TestShareService_SubmitShare(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	resp, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("secret-share-0"),
		ServerURL:  "https://server:8443",
		GroupName:  "Root Custodians",
		Purpose:    "barrier unseal",
		TenantID:   "t1",
	})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
}

func TestShareService_SubmitShare_EmptyGroupID(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:   "",
		ShareData: []byte("data"),
	})
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestShareService_SubmitShare_EmptyShareData(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:   "g1",
		ShareData: nil,
	})
	assert.ErrorIs(t, err, ErrEmptyShareData)
}

func TestShareService_SubmitShare_Duplicate(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	req := &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("secret-share-0"),
	}

	_, err := svc.SubmitShare(ctx, req)
	require.NoError(t, err)

	_, err = svc.SubmitShare(ctx, req)
	assert.ErrorIs(t, err, ErrShareAlreadySubmitted)
}

func TestShareService_SubmitShare_DifferentIndices(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("share-0"),
	})
	require.NoError(t, err)

	_, err = svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 1,
		ShareData:  []byte("share-1"),
	})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// ListShares tests
// ---------------------------------------------------------------------------

func TestShareService_ListShares(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	// Empty initially.
	shares, err := svc.ListShares(ctx)
	require.NoError(t, err)
	assert.Empty(t, shares)

	// Submit a few shares.
	_, err = svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("share-0"),
		GroupName:  "Group 1",
	})
	require.NoError(t, err)

	_, err = svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 1,
		ShareData:  []byte("share-1"),
		GroupName:  "Group 1",
	})
	require.NoError(t, err)

	_, err = svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g2",
		ShareIndex: 0,
		ShareData:  []byte("share-g2-0"),
		GroupName:  "Group 2",
	})
	require.NoError(t, err)

	shares, err = svc.ListShares(ctx)
	require.NoError(t, err)
	assert.Len(t, shares, 3)

	// Verify data integrity of one share.
	found := false
	for _, s := range shares {
		if s.GroupID == "g2" && s.ShareIndex == 0 {
			assert.Equal(t, []byte("share-g2-0"), s.ShareData)
			assert.Equal(t, "Group 2", s.GroupName)
			assert.False(t, s.ReceivedAt.IsZero())
			found = true
		}
	}
	assert.True(t, found, "expected to find g2 share index 0")
}

// ---------------------------------------------------------------------------
// GetShareCollectionStatus tests
// ---------------------------------------------------------------------------

func TestShareService_GetShareCollectionStatus(t *testing.T) {
	store := seal.NewMemoryBackend()
	groupSvc := newTestGroupServiceWithStore(t, store)
	shareSvc := newTestShareServiceWithStore(t, store, groupSvc)
	ctx := context.Background()

	// Create a group with threshold=2, total=3.
	_, err := groupSvc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)
	_, err = groupSvc.AddMember(ctx, "g1", "u1", "alice", "email")
	require.NoError(t, err)
	_, err = groupSvc.AddMember(ctx, "g1", "u2", "bob", "email")
	require.NoError(t, err)
	_, err = groupSvc.AddMember(ctx, "g1", "u3", "charlie", "email")
	require.NoError(t, err)

	// No shares submitted yet.
	status, err := shareSvc.GetShareCollectionStatus(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, "g1", status.GroupID)
	assert.Equal(t, 2, status.Threshold)
	assert.Equal(t, 3, status.Total)
	assert.Equal(t, 0, status.Collected)
	assert.False(t, status.Ready)

	// Submit one share - not yet at threshold.
	_, err = shareSvc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("share-0"),
	})
	require.NoError(t, err)

	status, err = shareSvc.GetShareCollectionStatus(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, 1, status.Collected)
	assert.False(t, status.Ready)

	// Submit second share - now at threshold.
	_, err = shareSvc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 1,
		ShareData:  []byte("share-1"),
	})
	require.NoError(t, err)

	status, err = shareSvc.GetShareCollectionStatus(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, 2, status.Collected)
	assert.True(t, status.Ready)

	// Submit third share - still ready.
	_, err = shareSvc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 2,
		ShareData:  []byte("share-2"),
	})
	require.NoError(t, err)

	status, err = shareSvc.GetShareCollectionStatus(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, 3, status.Collected)
	assert.True(t, status.Ready)
}

func TestShareService_GetShareCollectionStatus_EmptyGroupID(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.GetShareCollectionStatus(ctx, "")
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestShareService_GetShareCollectionStatus_GroupNotFound(t *testing.T) {
	store := seal.NewMemoryBackend()
	groupSvc := newTestGroupServiceWithStore(t, store)
	shareSvc := newTestShareServiceWithStore(t, store, groupSvc)
	ctx := context.Background()

	_, err := shareSvc.GetShareCollectionStatus(ctx, "nonexistent")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

func TestShareService_GetShareCollectionStatus_NilGroupSvc(t *testing.T) {
	// When groupSvc is nil, threshold/total are 0 and Ready is always false.
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("share-0"),
	})
	require.NoError(t, err)

	status, err := svc.GetShareCollectionStatus(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, 1, status.Collected)
	assert.Equal(t, 0, status.Threshold)
	assert.Equal(t, 0, status.Total)
	assert.False(t, status.Ready)
}

// ---------------------------------------------------------------------------
// GetShare / DeleteShare tests
// ---------------------------------------------------------------------------

func TestShareService_GetShare_RoundTrip(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		ServerURL:  "https://server:8443",
		GroupID:    "g1",
		GroupName:  "Root Custodians",
		ShareIndex: 2,
		ShareData:  []byte("secret-share-2"),
		Purpose:    "barrier unseal",
		TenantID:   "t1",
	})
	require.NoError(t, err)

	got, err := svc.GetShare(ctx, "https://server:8443", "g1", 2)
	require.NoError(t, err)
	assert.Equal(t, "https://server:8443", got.ServerURL)
	assert.Equal(t, "g1", got.GroupID)
	assert.Equal(t, 2, got.ShareIndex)
	assert.Equal(t, []byte("secret-share-2"), got.ShareData)
	assert.Equal(t, "t1", got.TenantID)

	// Deleting removes the share; a subsequent Get returns not found.
	require.NoError(t, svc.DeleteShare(ctx, "https://server:8443", "g1", 2))

	_, err = svc.GetShare(ctx, "https://server:8443", "g1", 2)
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestShareService_GetShare_EmptyServerURLMatchesAny(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		ServerURL:  "https://server:8443",
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("s0"),
	})
	require.NoError(t, err)

	// An empty serverURL is treated as a wildcard and matches the stored share.
	got, err := svc.GetShare(ctx, "", "g1", 0)
	require.NoError(t, err)
	assert.Equal(t, "https://server:8443", got.ServerURL)
}

func TestShareService_GetShare_ServerURLMismatch(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		ServerURL:  "https://server:8443",
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("s0"),
	})
	require.NoError(t, err)

	_, err = svc.GetShare(ctx, "https://other:8443", "g1", 0)
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestShareService_GetShare_NotFound(t *testing.T) {
	svc := newTestShareService(t)
	_, err := svc.GetShare(context.Background(), "https://server:8443", "missing", 0)
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestShareService_GetShare_EmptyGroupID(t *testing.T) {
	svc := newTestShareService(t)
	_, err := svc.GetShare(context.Background(), "https://server:8443", "", 0)
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestShareService_DeleteShare_NotFound(t *testing.T) {
	svc := newTestShareService(t)
	err := svc.DeleteShare(context.Background(), "https://server:8443", "missing", 0)
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestShareService_DeleteShare_EmptyGroupID(t *testing.T) {
	svc := newTestShareService(t)
	err := svc.DeleteShare(context.Background(), "https://server:8443", "", 0)
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestShareService_DeleteShare_ServerURLMismatch(t *testing.T) {
	svc := newTestShareService(t)
	ctx := context.Background()

	_, err := svc.SubmitShare(ctx, &transport.SubmitShareRequest{
		ServerURL:  "https://server:8443",
		GroupID:    "g1",
		ShareIndex: 0,
		ShareData:  []byte("s0"),
	})
	require.NoError(t, err)

	// Mismatched serverURL must not delete the share.
	err = svc.DeleteShare(ctx, "https://other:8443", "g1", 0)
	assert.ErrorIs(t, err, ErrShareNotFound)

	// The share is still present.
	_, err = svc.GetShare(ctx, "https://server:8443", "g1", 0)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestShareServiceError(t *testing.T) {
	inner := errors.New("io error")
	err := &ShareServiceError{Op: "put", Err: inner}
	assert.Contains(t, err.Error(), "share put failed")
	assert.Contains(t, err.Error(), "io error")
	assert.ErrorIs(t, err, inner)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestShareService(t *testing.T) *ShareService {
	t.Helper()
	store := seal.NewMemoryBackend()
	svc, err := NewShareService(store, nil, testLogger())
	require.NoError(t, err)
	return svc
}

func newTestShareServiceWithStore(t *testing.T, store seal.StorageBackend, groupSvc CustodianGroupServicer) *ShareService {
	t.Helper()
	svc, err := NewShareService(store, groupSvc, testLogger())
	require.NoError(t, err)
	return svc
}

func newTestGroupServiceWithStore(t *testing.T, store seal.StorageBackend) *CustodianGroupService {
	t.Helper()
	svc, err := NewCustodianGroupService(store, testLogger())
	require.NoError(t, err)
	return svc
}
