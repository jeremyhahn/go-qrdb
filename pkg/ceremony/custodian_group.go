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
	"log/slog"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	// custodianGroupPrefix is the storage key prefix for custodian groups.
	custodianGroupPrefix = "custodian-groups/"
)

// CustodianGroupServicer defines the interface for custodian group management.
type CustodianGroupServicer interface {
	// CreateGroup creates a new custodian group.
	CreateGroup(ctx context.Context, id, tenantID, name, purpose string, threshold, total int) (*transport.CustodianGroupInfo, error)

	// GetGroup retrieves a custodian group by ID.
	GetGroup(ctx context.Context, groupID string) (*transport.CustodianGroupInfo, error)

	// ListGroups lists all custodian groups.
	ListGroups(ctx context.Context) ([]transport.CustodianGroupInfo, error)

	// DeleteGroup deletes a custodian group by ID.
	DeleteGroup(ctx context.Context, groupID string) error

	// AddMember adds a member to a custodian group.
	AddMember(ctx context.Context, groupID, userID, username, method string) (*transport.CustodianMemberInfo, error)

	// RemoveMember removes a member from a custodian group.
	RemoveMember(ctx context.Context, groupID, userID string) error

	// DistributeShares marks shares as distributed for the group.
	DistributeShares(ctx context.Context, groupID string) (int, error)
}

// CustodianGroupService manages custodian groups for Shamir share
// distribution. Groups track which humans will hold shares and their
// membership state.
type CustodianGroupService struct {
	store  seal.StorageBackend
	logger *slog.Logger
	mu     sync.RWMutex
}

// Compile-time interface check.
var _ CustodianGroupServicer = (*CustodianGroupService)(nil)

// NewCustodianGroupService creates a new CustodianGroupService backed by
// the given StorageBackend. Returns ErrNilStore if store is nil, or
// ErrNilLogger if logger is nil.
func NewCustodianGroupService(store seal.StorageBackend, logger *slog.Logger) (*CustodianGroupService, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	return &CustodianGroupService{
		store:  store,
		logger: logger,
	}, nil
}

// CreateGroup creates a new custodian group with the given parameters.
// Returns ErrEmptyGroupID if id is empty, ErrEmptyGroupName if name is
// empty, ErrInvalidTotal if total < 1, ErrInvalidThreshold if threshold
// is invalid, and ErrGroupAlreadyExists if a group with the same ID
// already exists.
func (s *CustodianGroupService) CreateGroup(
	ctx context.Context,
	id, tenantID, name, purpose string,
	threshold, total int,
) (*transport.CustodianGroupInfo, error) {
	if id == "" {
		return nil, ErrEmptyGroupID
	}
	if name == "" {
		return nil, ErrEmptyGroupName
	}
	if total < 1 {
		return nil, ErrInvalidTotal
	}
	if threshold < 1 || threshold > total {
		return nil, ErrInvalidThreshold
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := custodianGroupPrefix + id

	exists, err := s.store.Exists(ctx, key)
	if err != nil {
		return nil, &GroupStoreError{Op: "exists-check", Err: err}
	}
	if exists {
		return nil, ErrGroupAlreadyExists
	}

	now := time.Now().UTC()
	group := &transport.CustodianGroupInfo{
		ID:        id,
		TenantID:  tenantID,
		Name:      name,
		Purpose:   purpose,
		Threshold: threshold,
		Total:     total,
		Members:   []transport.CustodianMemberInfo{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.putGroup(ctx, key, group); err != nil {
		return nil, err
	}

	s.logger.Info("custodian group created",
		slog.String("id", id),
		slog.String("name", name),
		slog.Int("threshold", threshold),
		slog.Int("total", total))

	return group, nil
}

// GetGroup retrieves a custodian group by ID. Returns ErrEmptyGroupID if
// groupID is empty, or ErrGroupNotFound if the group does not exist.
func (s *CustodianGroupService) GetGroup(ctx context.Context, groupID string) (*transport.CustodianGroupInfo, error) {
	if groupID == "" {
		return nil, ErrEmptyGroupID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getGroup(ctx, custodianGroupPrefix+groupID)
}

// ListGroups returns all custodian groups.
func (s *CustodianGroupService) ListGroups(ctx context.Context) ([]transport.CustodianGroupInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys, err := s.store.List(ctx, custodianGroupPrefix)
	if err != nil {
		return nil, &GroupStoreError{Op: "list", Err: err}
	}

	groups := make([]transport.CustodianGroupInfo, 0, len(keys))
	for _, key := range keys {
		group, err := s.getGroup(ctx, key)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *group)
	}

	return groups, nil
}

// DeleteGroup deletes a custodian group by ID. Returns ErrEmptyGroupID
// if groupID is empty, or ErrGroupNotFound if the group does not exist.
func (s *CustodianGroupService) DeleteGroup(ctx context.Context, groupID string) error {
	if groupID == "" {
		return ErrEmptyGroupID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := custodianGroupPrefix + groupID

	exists, err := s.store.Exists(ctx, key)
	if err != nil {
		return &GroupStoreError{Op: "exists-check", Err: err}
	}
	if !exists {
		return ErrGroupNotFound
	}

	if err := s.store.Delete(ctx, key); err != nil {
		return &GroupStoreError{Op: "delete", Err: err}
	}

	s.logger.Info("custodian group deleted", slog.String("id", groupID))

	return nil
}

// AddMember adds a member to the specified custodian group. Returns
// ErrEmptyGroupID if groupID is empty, ErrGroupNotFound if the group
// does not exist, ErrGroupFull if the group has reached capacity, and
// ErrMemberAlreadyExists if the user is already a member.
func (s *CustodianGroupService) AddMember(
	ctx context.Context,
	groupID, userID, username, method string,
) (*transport.CustodianMemberInfo, error) {
	if groupID == "" {
		return nil, ErrEmptyGroupID
	}
	if userID == "" {
		return nil, ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := custodianGroupPrefix + groupID

	group, err := s.getGroup(ctx, key)
	if err != nil {
		return nil, err
	}

	if len(group.Members) >= group.Total {
		return nil, ErrGroupFull
	}

	for _, m := range group.Members {
		if m.UserID == userID {
			return nil, ErrMemberAlreadyExists
		}
	}

	member := transport.CustodianMemberInfo{
		ShareIndex: len(group.Members),
		UserID:     userID,
		Username:   username,
		AssignedAt: time.Now().UTC(),
		Method:     method,
	}

	group.Members = append(group.Members, member)
	group.UpdatedAt = time.Now().UTC()

	if err := s.putGroup(ctx, key, group); err != nil {
		return nil, err
	}

	s.logger.Info("custodian member added",
		slog.String("group_id", groupID),
		slog.String("user_id", userID),
		slog.String("username", username))

	return &member, nil
}

// RemoveMember removes a member from the specified custodian group.
// After removal, remaining members are reindexed to maintain
// contiguous share indices. Returns ErrEmptyGroupID if groupID is
// empty, ErrGroupNotFound if the group does not exist, and
// ErrMemberNotFound if the user is not a member.
func (s *CustodianGroupService) RemoveMember(ctx context.Context, groupID, userID string) error {
	if groupID == "" {
		return ErrEmptyGroupID
	}
	if userID == "" {
		return ErrUserNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := custodianGroupPrefix + groupID

	group, err := s.getGroup(ctx, key)
	if err != nil {
		return err
	}

	found := -1
	for i, m := range group.Members {
		if m.UserID == userID {
			found = i
			break
		}
	}
	if found < 0 {
		return ErrMemberNotFound
	}

	// Remove the member and reindex.
	group.Members = append(group.Members[:found], group.Members[found+1:]...)
	for i := range group.Members {
		group.Members[i].ShareIndex = i
	}
	group.UpdatedAt = time.Now().UTC()

	if err := s.putGroup(ctx, key, group); err != nil {
		return err
	}

	s.logger.Info("custodian member removed",
		slog.String("group_id", groupID),
		slog.String("user_id", userID))

	return nil
}

// DistributeShares marks shares as pending distribution for all members
// of the group. Requires that the group has at least Total members.
// Returns the number of shares distributed (equal to member count).
func (s *CustodianGroupService) DistributeShares(ctx context.Context, groupID string) (int, error) {
	if groupID == "" {
		return 0, ErrEmptyGroupID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := custodianGroupPrefix + groupID

	group, err := s.getGroup(ctx, key)
	if err != nil {
		return 0, err
	}

	if len(group.Members) < group.Total {
		return 0, ErrInsufficientMembers
	}

	// Mark all members' ReceivedAt as nil (pending distribution).
	for i := range group.Members {
		group.Members[i].ReceivedAt = nil
	}
	group.UpdatedAt = time.Now().UTC()

	if err := s.putGroup(ctx, key, group); err != nil {
		return 0, err
	}

	count := len(group.Members)

	s.logger.Info("shares distributed",
		slog.String("group_id", groupID),
		slog.Int("count", count))

	return count, nil
}

// getGroup loads a custodian group from the storage backend by its full
// key. Returns ErrGroupNotFound if the key does not exist.
func (s *CustodianGroupService) getGroup(ctx context.Context, key string) (*transport.CustodianGroupInfo, error) {
	data, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, ErrGroupNotFound
	}

	var group transport.CustodianGroupInfo
	if err := json.Unmarshal(data, &group); err != nil {
		return nil, &GroupStoreError{Op: "unmarshal", Err: err}
	}

	return &group, nil
}

// putGroup serializes and stores a custodian group under the given key.
func (s *CustodianGroupService) putGroup(ctx context.Context, key string, group *transport.CustodianGroupInfo) error {
	data, err := json.Marshal(group)
	if err != nil {
		return &GroupStoreError{Op: "marshal", Err: err}
	}
	if err := s.store.Put(ctx, key, data); err != nil {
		return &GroupStoreError{Op: "put", Err: err}
	}
	return nil
}
