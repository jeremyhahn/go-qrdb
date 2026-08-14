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
)

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewCustodianGroupService(t *testing.T) {
	store := seal.NewMemoryBackend()
	logger := testLogger()

	svc, err := NewCustodianGroupService(store, logger)
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewCustodianGroupService_NilStore(t *testing.T) {
	_, err := NewCustodianGroupService(nil, testLogger())
	assert.ErrorIs(t, err, ErrNilStore)
}

func TestNewCustodianGroupService_NilLogger(t *testing.T) {
	_, err := NewCustodianGroupService(seal.NewMemoryBackend(), nil)
	assert.ErrorIs(t, err, ErrNilLogger)
}

// ---------------------------------------------------------------------------
// CreateGroup tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_CreateGroup(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	group, err := svc.CreateGroup(ctx, "g1", "tenant-1", "Root Key Custodians", "barrier unseal", 3, 5)
	require.NoError(t, err)
	assert.Equal(t, "g1", group.ID)
	assert.Equal(t, "tenant-1", group.TenantID)
	assert.Equal(t, "Root Key Custodians", group.Name)
	assert.Equal(t, "barrier unseal", group.Purpose)
	assert.Equal(t, 3, group.Threshold)
	assert.Equal(t, 5, group.Total)
	assert.Empty(t, group.Members)
	assert.False(t, group.CreatedAt.IsZero())
	assert.False(t, group.UpdatedAt.IsZero())
}

func TestCustodianGroupService_CreateGroup_EmptyID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "", "t1", "name", "purpose", 2, 3)
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestCustodianGroupService_CreateGroup_EmptyName(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "", "purpose", 2, 3)
	assert.ErrorIs(t, err, ErrEmptyGroupName)
}

func TestCustodianGroupService_CreateGroup_InvalidTotal(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 0)
	assert.ErrorIs(t, err, ErrInvalidTotal)
}

func TestCustodianGroupService_CreateGroup_ThresholdExceedsTotal(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 5, 3)
	assert.ErrorIs(t, err, ErrInvalidThreshold)
}

func TestCustodianGroupService_CreateGroup_ThresholdZero(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 0, 3)
	assert.ErrorIs(t, err, ErrInvalidThreshold)
}

func TestCustodianGroupService_CreateGroup_Duplicate(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	_, err = svc.CreateGroup(ctx, "g1", "t1", "name2", "purpose2", 2, 3)
	assert.ErrorIs(t, err, ErrGroupAlreadyExists)
}

// ---------------------------------------------------------------------------
// GetGroup tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_GetGroup(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	created, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	got, err := svc.GetGroup(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.Name, got.Name)
}

func TestCustodianGroupService_GetGroup_EmptyID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.GetGroup(ctx, "")
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestCustodianGroupService_GetGroup_NotFound(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.GetGroup(ctx, "nonexistent")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

// ---------------------------------------------------------------------------
// ListGroups tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_ListGroups(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	// Empty list initially.
	groups, err := svc.ListGroups(ctx)
	require.NoError(t, err)
	assert.Empty(t, groups)

	_, err = svc.CreateGroup(ctx, "g1", "t1", "group1", "p1", 2, 3)
	require.NoError(t, err)
	_, err = svc.CreateGroup(ctx, "g2", "t1", "group2", "p2", 3, 5)
	require.NoError(t, err)

	groups, err = svc.ListGroups(ctx)
	require.NoError(t, err)
	assert.Len(t, groups, 2)
}

// ---------------------------------------------------------------------------
// DeleteGroup tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_DeleteGroup(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	err = svc.DeleteGroup(ctx, "g1")
	require.NoError(t, err)

	_, err = svc.GetGroup(ctx, "g1")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

func TestCustodianGroupService_DeleteGroup_EmptyID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	err := svc.DeleteGroup(ctx, "")
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestCustodianGroupService_DeleteGroup_NotFound(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	err := svc.DeleteGroup(ctx, "nonexistent")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

// ---------------------------------------------------------------------------
// AddMember tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_AddMember(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	member, err := svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)
	assert.Equal(t, 0, member.ShareIndex)
	assert.Equal(t, "user-1", member.UserID)
	assert.Equal(t, "alice", member.Username)
	assert.Equal(t, "email", member.Method)
	assert.False(t, member.AssignedAt.IsZero())
	assert.Nil(t, member.ReceivedAt)

	member2, err := svc.AddMember(ctx, "g1", "user-2", "bob", "in-person")
	require.NoError(t, err)
	assert.Equal(t, 1, member2.ShareIndex)

	// Verify the group was updated.
	group, err := svc.GetGroup(ctx, "g1")
	require.NoError(t, err)
	assert.Len(t, group.Members, 2)
}

func TestCustodianGroupService_AddMember_EmptyGroupID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.AddMember(ctx, "", "user-1", "alice", "email")
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestCustodianGroupService_AddMember_EmptyUserID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "", "alice", "email")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestCustodianGroupService_AddMember_GroupNotFound(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.AddMember(ctx, "nonexistent", "user-1", "alice", "email")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

func TestCustodianGroupService_AddMember_GroupFull(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 1, 2)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "user-2", "bob", "email")
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-3", "charlie", "email")
	assert.ErrorIs(t, err, ErrGroupFull)
}

func TestCustodianGroupService_AddMember_Duplicate(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice-dup", "email")
	assert.ErrorIs(t, err, ErrMemberAlreadyExists)
}

// ---------------------------------------------------------------------------
// RemoveMember tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_RemoveMember(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 5)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "user-2", "bob", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "user-3", "charlie", "email")
	require.NoError(t, err)

	err = svc.RemoveMember(ctx, "g1", "user-2")
	require.NoError(t, err)

	// Verify reindexing.
	group, err := svc.GetGroup(ctx, "g1")
	require.NoError(t, err)
	assert.Len(t, group.Members, 2)
	assert.Equal(t, 0, group.Members[0].ShareIndex)
	assert.Equal(t, "user-1", group.Members[0].UserID)
	assert.Equal(t, 1, group.Members[1].ShareIndex)
	assert.Equal(t, "user-3", group.Members[1].UserID)
}

func TestCustodianGroupService_RemoveMember_EmptyGroupID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	err := svc.RemoveMember(ctx, "", "user-1")
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestCustodianGroupService_RemoveMember_EmptyUserID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	err = svc.RemoveMember(ctx, "g1", "")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestCustodianGroupService_RemoveMember_NotFound(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	err = svc.RemoveMember(ctx, "g1", "nonexistent")
	assert.ErrorIs(t, err, ErrMemberNotFound)
}

func TestCustodianGroupService_RemoveMember_GroupNotFound(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	err := svc.RemoveMember(ctx, "nonexistent", "user-1")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

// ---------------------------------------------------------------------------
// DistributeShares tests
// ---------------------------------------------------------------------------

func TestCustodianGroupService_DistributeShares(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "user-2", "bob", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "g1", "user-3", "charlie", "email")
	require.NoError(t, err)

	count, err := svc.DistributeShares(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	// Verify all ReceivedAt are nil.
	group, err := svc.GetGroup(ctx, "g1")
	require.NoError(t, err)
	for _, m := range group.Members {
		assert.Nil(t, m.ReceivedAt)
	}
}

func TestCustodianGroupService_DistributeShares_EmptyGroupID(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.DistributeShares(ctx, "")
	assert.ErrorIs(t, err, ErrEmptyGroupID)
}

func TestCustodianGroupService_DistributeShares_GroupNotFound(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.DistributeShares(ctx, "nonexistent")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

func TestCustodianGroupService_DistributeShares_InsufficientMembers(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, "g1", "t1", "name", "purpose", 2, 3)
	require.NoError(t, err)

	_, err = svc.AddMember(ctx, "g1", "user-1", "alice", "email")
	require.NoError(t, err)

	_, err = svc.DistributeShares(ctx, "g1")
	assert.ErrorIs(t, err, ErrInsufficientMembers)
}

// ---------------------------------------------------------------------------
// Full CRUD lifecycle test
// ---------------------------------------------------------------------------

func TestCustodianGroupService_FullLifecycle(t *testing.T) {
	svc := newTestGroupService(t)
	ctx := context.Background()

	// Create.
	group, err := svc.CreateGroup(ctx, "lifecycle", "t1", "Lifecycle Group", "test", 2, 3)
	require.NoError(t, err)
	assert.Equal(t, "lifecycle", group.ID)

	// Get.
	got, err := svc.GetGroup(ctx, "lifecycle")
	require.NoError(t, err)
	assert.Equal(t, group.Name, got.Name)

	// List.
	groups, err := svc.ListGroups(ctx)
	require.NoError(t, err)
	assert.Len(t, groups, 1)

	// Add members.
	_, err = svc.AddMember(ctx, "lifecycle", "u1", "alice", "email")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "lifecycle", "u2", "bob", "in-person")
	require.NoError(t, err)
	_, err = svc.AddMember(ctx, "lifecycle", "u3", "charlie", "usb")
	require.NoError(t, err)

	// Remove a member.
	err = svc.RemoveMember(ctx, "lifecycle", "u2")
	require.NoError(t, err)

	got, err = svc.GetGroup(ctx, "lifecycle")
	require.NoError(t, err)
	assert.Len(t, got.Members, 2)

	// Add replacement member.
	_, err = svc.AddMember(ctx, "lifecycle", "u4", "dave", "email")
	require.NoError(t, err)

	// Distribute.
	count, err := svc.DistributeShares(ctx, "lifecycle")
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	// Delete.
	err = svc.DeleteGroup(ctx, "lifecycle")
	require.NoError(t, err)

	_, err = svc.GetGroup(ctx, "lifecycle")
	assert.ErrorIs(t, err, ErrGroupNotFound)
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestGroupStoreError(t *testing.T) {
	inner := errors.New("disk full")
	err := &GroupStoreError{Op: "put", Err: inner}
	assert.Contains(t, err.Error(), "custodian group put failed")
	assert.Contains(t, err.Error(), "disk full")
	assert.ErrorIs(t, err, inner)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestGroupService(t *testing.T) *CustodianGroupService {
	t.Helper()
	store := seal.NewMemoryBackend()
	svc, err := NewCustodianGroupService(store, testLogger())
	require.NoError(t, err)
	return svc
}
