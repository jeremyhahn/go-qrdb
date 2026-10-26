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
	"slices"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
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
// expiring. Returns the refreshed lease with updated TTL. On the leader the
// renewal is made in memory, as etcd's is; anywhere else, and for a lease the
// leader has checkpointed, it is a replicated command.
//
// Until every replica of the shard can apply a lease checkpoint
// (CheckpointEnabled), every renewal is a replicated command, the leader's
// included: a replica that predates the lease-checkpoint feature counts a
// lease only from the log, so on becoming leader it would revoke a lease
// whose renewals had been made in its predecessor's memory.
func (s *LeaseService) KeepAlive(ctx context.Context, leaseID int64) (*Lease, error) {
	if !s.CheckpointEnabled() {
		return s.keepAliveReplicated(ctx, leaseID)
	}
	l, err := s.renewLocal(ctx, leaseID)
	var notPrimary *lease.NotPrimaryError
	var checkpointed *lease.CheckpointedError
	if !errors.As(err, &notPrimary) && !errors.As(err, &checkpointed) {
		return l, err
	}

	// The replica that answered is not the lease primary (a follower, a state
	// machine that does not renew in memory, or a primary that stood down
	// while the expired lease awaited its revoke), or the lease has a
	// checkpoint that only a replicated renewal clears: renew through the log,
	// which Raft forwards to the leader.
	return s.keepAliveReplicated(ctx, leaseID)
}

// renewLocal renews the lease in the memory of the replica a linearizable read
// lands on, which is the leader when the read is confirmed there. The renewal
// never reaches the Raft log; what a new leader learns of it is bounded by the
// leader's checkpoints (see lease.Lessor). A *lease.NotPrimaryError or
// *lease.CheckpointedError comes back unwrapped so KeepAlive can fall back to
// the log.
func (s *LeaseService) renewLocal(ctx context.Context, leaseID int64) (*Lease, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, LeaseKeepAliveQuery{ID: leaseID})
	if err != nil {
		var notPrimary *lease.NotPrimaryError
		if errors.As(err, &notPrimary) {
			return nil, notPrimary
		}
		var checkpointed *lease.CheckpointedError
		if errors.As(err, &checkpointed) {
			return nil, checkpointed
		}
		var expired *lease.ExpiredError
		if errors.As(err, &expired) {
			return nil, s.awaitExpired(ctx, expired)
		}
		var notFound *lease.NotFoundError
		if errors.As(err, &notFound) {
			return nil, &LeaseNotFoundError{LeaseID: notFound.ID}
		}
		return nil, s.translateReadError(err)
	}

	l, ok := result.(*Lease)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return l, nil
}

// awaitExpired waits, outside the read that found it, for the fate of a lease
// the primary refused to renew because it is past its deadline, as etcd's
// lessor.Renew does. Its revoke applying answers *LeaseNotFoundError, never a
// renewal the revoke would overtake; the primary standing down answers
// *lease.NotPrimaryError, so KeepAlive renews through the log and the next
// primary decides; ctx ending first is a read error like any other.
func (s *LeaseService) awaitExpired(ctx context.Context, expired *lease.ExpiredError) error {
	err := expired.Await(ctx)
	var notFound *lease.NotFoundError
	if errors.As(err, &notFound) {
		return &LeaseNotFoundError{LeaseID: notFound.ID}
	}
	var notPrimary *lease.NotPrimaryError
	if errors.As(err, &notPrimary) {
		return notPrimary
	}
	return s.translateReadError(err)
}

// keepAliveReplicated renews the lease with a replicated keep-alive command,
// which every replica applies.
//
// The apply that resets the lease already computes its post-reset state
// (go-qrdb#275): the state machine carries it back in the proposal's
// Result.Data, so this builds the response directly from that committed
// result instead of issuing a second, separately linearized read. That
// second read was this path's tail latency -- a ReadIndex round trip
// whenever the leader's local read lease was not valid when the follow-up
// landed -- on top of the proposal's own round trip.
func (s *LeaseService) keepAliveReplicated(ctx context.Context, leaseID int64) (*Lease, error) {
	result, err := s.proposeKeepAlive(ctx, leaseID)
	if err != nil {
		return nil, err
	}

	// A KeepAlive against a lease the lessor does not (or no longer) hold is
	// an idempotent apply success, not an application error (see
	// protocol.EncodeLeaseKeepAliveNotFound), so proposeKeepAlive's
	// checkResultError never sees it; it is decoded here instead and reported to the caller
	// the same way TimeToLive's not-found case is -- unwrapped, so every
	// transport's typed classifier still sees *LeaseNotFoundError.
	if id, notFound := protocol.DecodeLeaseKeepAliveNotFound(result.Data); notFound {
		return nil, &LeaseNotFoundError{LeaseID: id}
	}

	id, grantedTTL, remainingTTL, keys, ok := protocol.DecodeLeaseInfo(result.Data)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return &lease.Lease{
		ID:           id,
		TTL:          grantedTTL,
		GrantedTTL:   grantedTTL,
		RemainingTTL: remainingTTL,
		Keys:         keys,
	}, nil
}

// CheckpointEnabled reports whether every replica of the shard can apply a
// lease checkpoint (the lease-checkpoint cluster feature). Until it can, the
// leader checkpoints nothing, and a new leader counts every lease from its
// full TTL, as it always has.
func (s *LeaseService) CheckpointEnabled() bool {
	if gated, ok := s.host.(FeatureGatedHost); ok && !gated.FeatureEnabled(s.shardID, featuregate.LeaseCheckpoint) {
		return false
	}
	return true
}

// Checkpoint proposes the leader's checkpoints, carrying its round token and
// the Raft index they were sampled at, in commands of at most
// protocol.MaxLeaseCheckpointBatch leases each, and returns once this replica
// has applied them, or with the first error.
func (s *LeaseService) Checkpoint(ctx context.Context, token, index uint64, cps []lease.Checkpoint) error {
	for batch := range slices.Chunk(cps, protocol.MaxLeaseCheckpointBatch) {
		cmd, err := protocol.EncodeLeaseCheckpoint(token, index, batch)
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
	}
	return nil
}

// AwaitApplied returns once this replica has applied every entry the shard
// committed before the call. It is a linearizable read, and the leader serves
// one only once it has committed an entry of its own term, so a new leader
// that returns from it has applied every checkpoint its predecessor committed.
// The read is the lease listing, which every lease-serving state machine
// answers; its result is discarded.
func (s *LeaseService) AwaitApplied(ctx context.Context) error {
	if _, err := s.host.SyncRead(ctx, s.shardID, LeasesQuery{}); err != nil {
		return s.translateReadError(err)
	}
	return nil
}

// CheckpointAll asks this node's replica, if it is the shard's lease primary,
// to checkpoint every live lease now, and waits until the checkpoint has been
// applied here. A leader calls it before handing leadership to another
// replica, as etcd does, so the next leader counts each lease from what it has
// left rather than from its full TTL. A replica that is not the lease primary
// answers *lease.NotPrimaryError, unwrapped: there is nothing on it to
// checkpoint.
func (s *LeaseService) CheckpointAll(ctx context.Context) error {
	result, err := s.host.StaleRead(ctx, s.shardID, LeaseCheckpointNowQuery{})
	if err != nil {
		var notPrimary *lease.NotPrimaryError
		if errors.As(err, &notPrimary) {
			return notPrimary
		}
		return s.translateReadError(err)
	}
	answer, ok := result.(<-chan error)
	if !ok {
		return &unexpectedResultTypeError{}
	}
	select {
	case err := <-answer:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// proposeKeepAlive proposes a replicated keep-alive of the lease and returns
// the committed result, whose Data carries the lease's post-reset state.
func (s *LeaseService) proposeKeepAlive(ctx context.Context, leaseID int64) (sm.Result, error) {
	cmd, err := protocol.EncodeLeaseKeepAlive(leaseID)
	if err != nil {
		return sm.Result{}, &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return sm.Result{}, translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return sm.Result{}, &ProposeError{Err: resultErr}
	}
	return result, nil
}

// NewLeaseCheckpointer returns the lease.Checkpointer that replicates, for the
// named shard, its leader's checkpoints of the remaining TTL of its leases.
// Composers pass it to lease.WithCheckpoint.
func NewLeaseCheckpointer(host Host, shardID uint64) lease.Checkpointer {
	return &LeaseService{host: host, shardID: shardID}
}

// LeaseExpiryOptions returns the lease.RunExpiry options of a replicated
// shard: the checkpointer that replicates the leader's checkpoints through
// host, at the configured interval, and the failover grace. It fails with the
// config's typed error when either duration does not parse, which Validate
// already rejects at load.
func LeaseExpiryOptions(host Host, shardID uint64, cfg config.LeaseConfig) ([]lease.ExpiryOption, error) {
	interval, err := cfg.CheckpointIntervalDuration()
	if err != nil {
		return nil, err
	}
	grace, err := cfg.FailoverGraceDuration()
	if err != nil {
		return nil, err
	}
	return []lease.ExpiryOption{
		lease.WithCheckpoint(interval, NewLeaseCheckpointer(host, shardID)),
		lease.WithFailoverGrace(grace),
	}, nil
}

// TimeToLive returns the current state of a lease including its
// remaining TTL and attached keys.
//
// The leader renews leases in memory (KeepAlive), so a follower's deadline for
// a lease lags the leader's: only the leader answers the remaining TTL, as
// etcd's does. On a node whose replica follows, a lease it holds is answered
// with *LeaseTTLNotLeaderError naming the leader, which the transports forward
// the request to; a lease it does not hold is not found here as it is there.
func (s *LeaseService) TimeToLive(ctx context.Context, leaseID int64) (*Lease, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		var stale *lease.StaleTTLError
		if errors.As(err, &stale) {
			return nil, s.ttlNotLeader(ctx, stale.Lease)
		}
		return nil, s.translateReadError(err)
	}

	l, ok := result.(*Lease)
	if !ok {
		return nil, &unexpectedResultTypeError{}
	}
	return l, nil
}

// ttlNotLeader returns the refusal of a TimeToLive the shard's leader must
// answer, carrying this replica's view of lease l and naming the leader when
// this node knows it: its replica ID, and the Raft address the shard's
// membership records for it. A leader not yet known, or one the membership
// cannot be read for, leaves the address empty: the refusal is then answered
// as unavailable, as every not-leader refusal without a leader is, and the
// caller retries.
func (s *LeaseService) ttlNotLeader(ctx context.Context, l *Lease) error {
	refusal := &LeaseTTLNotLeaderError{NotLeaderError: NotLeaderError{ShardID: s.shardID}, Lease: l}
	leaderID, known, err := s.host.LeaderID(s.shardID)
	if err != nil || !known {
		return refusal
	}
	refusal.LeaderID = leaderID
	if m, err := s.host.SyncGetShardMembership(ctx, s.shardID); err == nil && m != nil {
		refusal.LeaderAddress = m.Nodes[leaderID]
	}
	return refusal
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
