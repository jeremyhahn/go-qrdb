// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package lease

import (
	"context"
	"log/slog"
	"time"
)

// DefaultExpiryInterval is the default cadence at which the leader sweeps
// for and revokes expired leases.
const DefaultExpiryInterval = time.Second

const (
	// DefaultCheckpointInterval is the default cadence at which the leader
	// checkpoints the remaining TTL of its long-lived leases. It is etcd's
	// default.
	DefaultCheckpointInterval = 5 * time.Minute

	// DefaultFailoverGrace is the default time added, beyond a lease's
	// effective TTL, to every lease's deadline when a replica becomes leader
	// (see Promote).
	DefaultFailoverGrace = 5 * time.Second
)

// Checkpointer replicates the leader's checkpoints. LeaseService implements it
// over the shard's Raft group.
type Checkpointer interface {
	// CheckpointEnabled reports whether every replica of the group can apply
	// a checkpoint, so one may be proposed.
	CheckpointEnabled() bool

	// Checkpoint proposes cps, carrying token and the Raft index they were
	// sampled at, through consensus and returns once this replica has applied
	// them, or with the first error. Every replica applies a proposed
	// checkpoint with Lessor.ApplyCheckpoints.
	Checkpoint(ctx context.Context, token, index uint64, cps []Checkpoint) error

	// AwaitApplied returns once this replica has applied every entry the
	// group committed before the call -- a linearizable read -- or with the
	// error that stopped it. A replica that has just become leader awaits it
	// before it promotes, so it counts each lease from the checkpoints its
	// predecessor committed, as etcd promotes its lessor only once the new
	// leader has applied the first entry of its term.
	AwaitApplied(ctx context.Context) error
}

// expiryConfig is what the ExpiryOptions of RunExpiry set.
type expiryConfig struct {
	grace              time.Duration
	checkpointInterval time.Duration
	checkpointer       Checkpointer
	hold               func() bool
}

// ExpiryOption configures RunExpiry.
type ExpiryOption func(*expiryConfig)

// WithCheckpoint makes RunExpiry checkpoint, through cp once per interval, the
// remaining TTL of every lease whose effective TTL is longer than interval,
// and serve on-demand checkpoints (RequestCheckpoint). Without it nothing is
// checkpointed, which is right only where there is no other replica to
// inform. An interval <= 0 uses DefaultCheckpointInterval.
func WithCheckpoint(interval time.Duration, cp Checkpointer) ExpiryOption {
	return func(c *expiryConfig) {
		if interval <= 0 {
			interval = DefaultCheckpointInterval
		}
		c.checkpointInterval = interval
		c.checkpointer = cp
	}
}

// WithFailoverGrace sets the extra time, beyond a lease's effective TTL,
// every lease gets when this replica becomes leader. It must not be negative.
func WithFailoverGrace(grace time.Duration) ExpiryOption {
	return func(c *expiryConfig) { c.grace = grace }
}

// RevokeFunc proposes revocation of a single lease through consensus so
// that every replica applies the deletion of the lease's attached keys
// and emits the corresponding watch notifications. It must route through
// the Raft proposal path (e.g. LeaseService.Revoke), not mutate the
// Lessor directly.
type RevokeFunc func(ctx context.Context, leaseID int64) error

// RunExpiry runs the leader-side lease-expiry sweeper until ctx is
// canceled, then returns. On each tick it consults isLeader; only while
// this node is the Raft leader does it revoke expired leases via revoke.
// Followers do not propose — they converge by applying the leader's
// replicated revoke commands — so this loop runs on every node but acts
// only when it leads (the etcd lessor model). Revoke errors (e.g. a
// transient not-leader race, or a lease already revoked) are logged at
// debug and retried on the next tick; once a revoke is applied locally
// the lease leaves the Lessor and is no longer reported as expired.
//
// With WithCheckpoint the same goroutine also runs the checkpoint rounds, on
// their own ticker, and the on-demand checkpoints RequestCheckpoint asks for.
// One goroutine does all three so that a replica's promotion and its
// checkpoint rounds never overlap, and so that its checkpoints are proposed --
// and therefore applied -- in the order their rounds were numbered. With it, a
// replica that becomes leader promotes (Promote) only once it has applied
// every entry its predecessor committed (Checkpointer.AwaitApplied); until then
// it neither sweeps nor checkpoints, and retries on the next tick.
//
// The tick runs on the Lessor's clock (see WithClock), so a test drives the
// sweep by advancing a clocktest.Fake rather than waiting out the interval.
//
// interval <= 0 uses DefaultExpiryInterval. A nil isLeader or revoke
// disables the sweep (the call returns immediately) — callers that want
// expiry MUST supply both. WithExpiryHold suspends the sweep while a
// condition holds, such as a sealed node barrier.
func (lr *Lessor) RunExpiry(ctx context.Context, interval time.Duration, isLeader func() bool, revoke RevokeFunc, logger *slog.Logger, opts ...ExpiryOption) {
	if interval <= 0 {
		interval = DefaultExpiryInterval
	}
	if isLeader == nil || revoke == nil {
		return
	}
	cfg := expiryConfig{grace: DefaultFailoverGrace}
	for _, opt := range opts {
		opt(&cfg)
	}

	lr.leader.Store(&isLeader)
	defer lr.leader.Store(nil)
	defer lr.endPrimary()

	var checkpointTick <-chan time.Time
	if cfg.checkpointer != nil {
		t := lr.clock.NewTicker(cfg.checkpointInterval)
		defer t.Stop()
		checkpointTick = t.C()
		lr.checkpointing.Store(true)
		defer func() {
			lr.checkpointing.Store(false)
			lr.answerCheckpointWaiters(&NotPrimaryError{})
		}()
	}

	ticker := lr.clock.NewTicker(interval)
	defer ticker.Stop()

	leading := isLeader()
	observe := func() bool {
		now := isLeader()
		if now && !leading {
			if cfg.checkpointer != nil {
				if err := cfg.checkpointer.AwaitApplied(ctx); err != nil {
					// Not yet promoted, so not yet leading: the next tick
					// tries again.
					if logger != nil {
						logger.Debug("lease expiry: promotion awaits the previous term",
							slog.Any("error", err))
					}
					return false
				}
			}
			lr.Promote(cfg.grace)
		}
		leading = now
		return leading
	}
	// holding reports whether WithExpiryHold holds the loop. The first call
	// that finds the hold released gives every lease a full TTL
	// (renewAfterHold) before anything is judged against its deadline.
	held := false
	holding := func() bool {
		if cfg.hold != nil && cfg.hold() {
			held = true
			return true
		}
		if held {
			held = false
			lr.renewAfterHold()
		}
		return false
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			// demotec is open exactly while ticks revoke expired leases, so
			// a renewal waiting on an expired lease (ExpiredError) learns
			// of either outcome: its revoke, or that no revoke comes from
			// here.
			if holding() || !observe() {
				lr.endPrimary()
				continue
			}
			lr.beginPrimary()
			for _, id := range lr.ExpiredLeaseIDs() {
				if err := revoke(ctx, id); err != nil {
					if logger != nil {
						logger.Debug("lease expiry: revoke failed",
							slog.Int64("lease_id", id),
							slog.Any("error", err))
					}
				}
			}
		case <-checkpointTick:
			if !holding() && observe() {
				// A periodic round has no waiter: proposeCheckpoints logs a
				// failure, and an unapplied lease keeps its pending mark, so
				// the next round proposes it again.
				_ = lr.checkpointRound(ctx, cfg, false, logger)
			}
		case <-lr.checkpointPoke:
			var err error = &NotPrimaryError{}
			if cfg.checkpointer != nil && !holding() && observe() {
				err = lr.checkpointRound(ctx, cfg, true, logger)
			}
			lr.answerCheckpointWaiters(err)
		}
	}
}

// RequestCheckpoint asks the checkpoint loop to checkpoint every live lease
// now, as etcd does before it hands leadership to another member, so that the
// next leader counts each lease from what it has left rather than from a full
// TTL. The returned channel receives the outcome once the checkpoint has been
// applied on this replica: nil, or the error that stopped it. A replica that
// is not the lease primary, or whose loop does not checkpoint, answers
// *NotPrimaryError without asking.
func (lr *Lessor) RequestCheckpoint() (<-chan error, error) {
	if !lr.checkpointing.Load() || !lr.isPrimary() {
		return nil, &NotPrimaryError{}
	}
	done := make(chan error, 1)
	lr.waitersMu.Lock()
	lr.checkpointWaiters = append(lr.checkpointWaiters, done)
	lr.waitersMu.Unlock()
	select {
	case lr.checkpointPoke <- struct{}{}:
	default: // a poke is already queued; it serves this request too
	}
	return done, nil
}

// answerCheckpointWaiters sends err to every pending RequestCheckpoint.
func (lr *Lessor) answerCheckpointWaiters(err error) {
	lr.waitersMu.Lock()
	waiters := lr.checkpointWaiters
	lr.checkpointWaiters = nil
	lr.waitersMu.Unlock()
	for _, w := range waiters {
		w <- err
	}
}

// checkpointRound runs one checkpoint round: it proposes the remaining TTL of
// every lease that is due -- every live lease when all is set -- stamped with
// the Raft index it was sampled at. It returns the first error, or
// *CheckpointsDisabledError when the group cannot apply checkpoints yet. A
// lease whose checkpoint was not applied keeps its pending mark, so the next
// round proposes it again and RenewLocal keeps refusing it until then.
func (lr *Lessor) checkpointRound(ctx context.Context, cfg expiryConfig, all bool, logger *slog.Logger) error {
	if !cfg.checkpointer.CheckpointEnabled() {
		return &CheckpointsDisabledError{}
	}
	recording := lr.recordFrom.Load() != 0
	lr.checkpointToken++
	index, due := lr.dueCheckpoints(lr.checkpointToken, cfg.checkpointInterval, all)
	err := lr.proposeCheckpoints(ctx, cfg.checkpointer, lr.checkpointToken, index, due, logger)
	if err != nil || recording || lr.recordFrom.Load() == 0 {
		return err
	}
	// The group's first checkpoint starts it recording renewals and is
	// rejected by every replica: no renewal before it was recorded, so none
	// that overtook its sample can be ruled out. Now that it has applied
	// here, the same leases are sampled again, so a round -- the one ahead of
	// a leadership transfer above all -- still lands its checkpoint.
	lr.checkpointToken++
	index, due = lr.dueCheckpoints(lr.checkpointToken, cfg.checkpointInterval, all)
	return lr.proposeCheckpoints(ctx, cfg.checkpointer, lr.checkpointToken, index, due, logger)
}

// proposeCheckpoints proposes cps through cp, logging a failure at debug.
func (lr *Lessor) proposeCheckpoints(ctx context.Context, cp Checkpointer, token, index uint64, cps []Checkpoint, logger *slog.Logger) error {
	if len(cps) == 0 {
		return nil
	}
	err := cp.Checkpoint(ctx, token, index, cps)
	if err != nil && logger != nil {
		logger.Debug("lease checkpoint: propose failed",
			slog.Int("leases", len(cps)),
			slog.Any("error", err))
	}
	return err
}

// dueCheckpoints returns the checkpoint of every live lease that is due in the
// round numbered token and marks each pending under it. A lease is due when
// its effective TTL is longer than interval -- etcd's rule: a lease that
// cannot outlive one interval gains nothing from a checkpoint -- when an
// earlier round's checkpoint of it has not been applied, or, when all is set,
// always. Each carries its remaining TTL as of now, rounded up to a whole
// second and capped at the granted TTL. index is the Raft index the sample is
// stamped with (see sampledIndex), read before any lease is.
func (lr *Lessor) dueCheckpoints(token uint64, interval time.Duration, all bool) (index uint64, due []Checkpoint) {
	index = lr.sampledIndex()

	lr.mu.RLock()
	defer lr.mu.RUnlock()

	now := lr.now()
	for id, l := range lr.leases {
		l.mu.Lock()
		remaining := l.expiry.Sub(now)
		if remaining <= 0 {
			l.mu.Unlock()
			continue // the sweep revokes it
		}
		if !all && l.pending == 0 && time.Duration(l.effectiveTTLLocked())*time.Second <= interval {
			l.mu.Unlock()
			continue
		}
		secs := min(int64((remaining+time.Second-1)/time.Second), l.ttl)
		l.pending = token
		l.mu.Unlock()
		due = append(due, Checkpoint{ID: id, RemainingTTL: secs})
	}
	return index, due
}

// WithExpiryHold suspends the sweep for as long as hold reports true. A held
// sweep revokes nothing, and when the hold is released every lease is given
// a full TTL from that moment before the sweep resumes, so a holder is not
// revoked for keep-alives this node could not take while it was held. A held
// loop proposes no checkpoint either, and answers RequestCheckpoint with
// *NotPrimaryError.
//
// The node composers pass the node barrier's IsSealed: a revoke is a Raft
// proposal, and while the node barrier is sealed the node's log cannot take
// one -- nor can it take the keep-alives the sweep would be judging leases
// by. A nil hold never holds.
func WithExpiryHold(hold func() bool) ExpiryOption {
	return func(c *expiryConfig) { c.hold = hold }
}
