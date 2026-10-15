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

package service

import (
	"context"
	"errors"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// LeaseServicer is an alias for the canonical lease.LeaseServicer interface.
type LeaseServicer = lease.LeaseServicer

// LeaseService implements LeaseServicer backed by a QuicRaft host
// running an MVCCStateMachine with an attached Lessor.
type LeaseService struct {
	host    Host
	shardID uint64
}

// NewLeaseService creates a new LeaseServicer that operates on the
// specified shard. The shard must be running an MVCCStateMachine with
// an attached Lessor for lease operations to succeed.
func NewLeaseService(host Host, shardID uint64) LeaseServicer {
	return &LeaseService{
		host:    host,
		shardID: shardID,
	}
}

// Grant creates a new lease with the specified TTL in seconds. The
// server assigns a unique lease ID. Returns the lease including the
// assigned ID, granted TTL, and remaining TTL.
func (s *LeaseService) Grant(ctx context.Context, ttl int64) (*Lease, error) {
	cmd, err := protocol.EncodeLeaseGrant(ttl)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return nil, translateProposeError(err)
	}

	// Check for an SM-encoded application error (e.g. lease subsystem not
	// configured on the target SM). The SM encodes these in result.Data
	// rather than returning an Update error that would stall Raft reads.
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, &ProposeError{Err: resultErr}
	}

	// The apply is the authority on what was granted: the Lessor stores the
	// requested ttl verbatim under the ID it assigned (result.Value), with
	// the whole ttl remaining and no keys yet, or refuses the command -- it
	// never adjusts the ttl. Answer with that lease rather than reading it
	// back: the expiry sweep may revoke a short lease between the apply and
	// any later read, and a read would then report a lease this call
	// granted as not found (DS-1689).
	return &Lease{ID: int64(result.Value), TTL: ttl, GrantedTTL: ttl, RemainingTTL: ttl}, nil
}

// Revoke immediately expires a lease and deletes all attached keys.
func (s *LeaseService) Revoke(ctx context.Context, leaseID int64) error {
	cmd, err := protocol.EncodeLeaseRevoke(leaseID)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return &ProposeError{Err: resultErr}
	}
	return nil
}

// KeepAlive resets the TTL on an existing lease, preventing it from
// expiring. Returns the refreshed lease with updated TTL.
func (s *LeaseService) KeepAlive(ctx context.Context, leaseID int64) (*Lease, error) {
	cmd, err := protocol.EncodeLeaseKeepAlive(leaseID)
	if err != nil {
		return nil, &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return nil, translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, &ProposeError{Err: resultErr}
	}

	// Read back the lease to get current state.
	return s.TimeToLive(ctx, leaseID)
}

// TimeToLive returns the current state of a lease including its
// remaining TTL and attached keys.
func (s *LeaseService) TimeToLive(ctx context.Context, leaseID int64) (*Lease, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	l, ok := result.(*Lease)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return l, nil
}

// Leases returns all active leases.
func (s *LeaseService) Leases(ctx context.Context) ([]*Lease, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, LeasesQuery{})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	leases, ok := result.([]*Lease)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return leases, nil
}

// translateReadError converts QuicRaft read errors to service-level
// typed errors.
func (s *LeaseService) translateReadError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}

	// A missing lease is a typed, actionable condition -- returned as-is,
	// the same way MVCCKVService.translateReadError returns KeyNotFoundError
	// unwrapped rather than folding it into the generic ReadError. Every
	// transport's classifier already has a *LeaseNotFoundError arm; wrapping
	// it in ReadError here would bury it behind that family's catch-all,
	// which is exactly the ordering bug those classifiers guard against for
	// every other typed cause.
	var lnf *LeaseNotFoundError
	if errors.As(err, &lnf) {
		return lnf
	}
	return &ReadError{Err: err}
}
