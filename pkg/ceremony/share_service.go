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
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	// sharePrefix is the storage key prefix for submitted shares.
	sharePrefix = "shares/"
)

// ShareServicer defines the interface for Shamir share submission and
// collection tracking.
type ShareServicer interface {
	// SubmitShare submits a Shamir share for a custodian group.
	SubmitShare(ctx context.Context, req *transport.SubmitShareRequest) (*transport.SubmitShareResponse, error)

	// ListShares lists all submitted shares.
	ListShares(ctx context.Context) ([]transport.ShareInfo, error)

	// GetShareCollectionStatus returns the collection progress for a group.
	GetShareCollectionStatus(ctx context.Context, groupID string) (*transport.ShareCollectionStatus, error)
}

// ShareService tracks the submission and collection of Shamir shares
// back to the server. When custodians are ready to unseal, they submit
// their shares. The service tracks progress toward the threshold.
type ShareService struct {
	store    seal.StorageBackend
	groupSvc CustodianGroupServicer
	logger   *slog.Logger
	mu       sync.RWMutex
}

// Compile-time interface check.
var _ ShareServicer = (*ShareService)(nil)

// NewShareService creates a new ShareService backed by the given
// StorageBackend and CustodianGroupServicer. The groupSvc is used to
// look up group threshold/total when computing collection status.
// Returns ErrNilStore if store is nil, ErrNilLogger if logger is nil.
func NewShareService(
	store seal.StorageBackend,
	groupSvc CustodianGroupServicer,
	logger *slog.Logger,
) (*ShareService, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	return &ShareService{
		store:    store,
		groupSvc: groupSvc,
		logger:   logger,
	}, nil
}

// SubmitShare stores a submitted Shamir share under
// "shares/{groupID}/{shareIndex}". Returns ErrEmptyGroupID if GroupID
// is empty, ErrEmptyShareData if ShareData is empty, and
// ErrShareAlreadySubmitted if a share already exists at the same key.
func (s *ShareService) SubmitShare(ctx context.Context, req *transport.SubmitShareRequest) (*transport.SubmitShareResponse, error) {
	if req.GroupID == "" {
		return nil, ErrEmptyGroupID
	}
	if len(req.ShareData) == 0 {
		return nil, ErrEmptyShareData
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.shareKey(req.GroupID, req.ShareIndex)

	exists, err := s.store.Exists(ctx, key)
	if err != nil {
		return nil, &ShareServiceError{Op: "exists-check", Err: err}
	}
	if exists {
		return nil, ErrShareAlreadySubmitted
	}

	info := &transport.ShareInfo{
		ServerURL:  req.ServerURL,
		GroupID:    req.GroupID,
		GroupName:  req.GroupName,
		ShareIndex: req.ShareIndex,
		ShareData:  req.ShareData,
		Purpose:    req.Purpose,
		ReceivedAt: time.Now().UTC(),
		TenantID:   req.TenantID,
	}

	data, err := json.Marshal(info)
	if err != nil {
		return nil, &ShareServiceError{Op: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, key, data); err != nil {
		return nil, &ShareServiceError{Op: "put", Err: err}
	}

	s.logger.Info("share submitted",
		slog.String("group_id", req.GroupID),
		slog.Int("share_index", req.ShareIndex))

	return &transport.SubmitShareResponse{Accepted: true}, nil
}

// ListShares returns all submitted shares across all groups.
func (s *ShareService) ListShares(ctx context.Context) ([]transport.ShareInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var shares []transport.ShareInfo
	if err := s.store.Scan(ctx, sharePrefix, func(_ string, data []byte) error {
		var info transport.ShareInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return &ShareServiceError{Op: "unmarshal", Err: err}
		}
		shares = append(shares, info)
		return nil
	}); err != nil {
		return nil, err
	}

	return shares, nil
}

// GetShareCollectionStatus returns the collection status for the given
// group. It counts submitted shares and compares against the group's
// threshold and total. Returns ErrEmptyGroupID if groupID is empty.
// If the groupSvc is nil, threshold and total are derived from
// submitted share metadata only and Ready is based on a default
// threshold of 0 (never ready without group context).
func (s *ShareService) GetShareCollectionStatus(ctx context.Context, groupID string) (*transport.ShareCollectionStatus, error) {
	if groupID == "" {
		return nil, ErrEmptyGroupID
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count shares submitted for this group.
	groupSharePrefix := sharePrefix + groupID + "/"
	keys, err := s.store.List(ctx, groupSharePrefix)
	if err != nil {
		return nil, &ShareServiceError{Op: "list", Err: err}
	}
	collected := len(keys)

	// Look up group metadata for threshold/total.
	var threshold, total int
	if s.groupSvc != nil {
		group, err := s.groupSvc.GetGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		threshold = group.Threshold
		total = group.Total
	}

	return &transport.ShareCollectionStatus{
		GroupID:   groupID,
		Threshold: threshold,
		Total:     total,
		Collected: collected,
		Ready:     collected >= threshold && threshold > 0,
	}, nil
}

// shareKey builds the storage key for a share.
func (s *ShareService) shareKey(groupID string, shareIndex int) string {
	var b strings.Builder
	b.WriteString(sharePrefix)
	b.WriteString(groupID)
	b.WriteString("/")
	fmt.Fprintf(&b, "%d", shareIndex)
	return b.String()
}
