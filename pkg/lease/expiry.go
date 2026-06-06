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
// interval <= 0 uses DefaultExpiryInterval. A nil isLeader or revoke
// disables the sweep (the call returns immediately) — callers that want
// expiry MUST supply both.
func (lr *Lessor) RunExpiry(ctx context.Context, interval time.Duration, isLeader func() bool, revoke RevokeFunc, logger *slog.Logger) {
	if interval <= 0 {
		interval = DefaultExpiryInterval
	}
	if isLeader == nil || revoke == nil {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !isLeader() {
				continue
			}
			for _, id := range lr.ExpiredLeaseIDs() {
				if err := revoke(ctx, id); err != nil {
					if logger != nil {
						logger.Debug("lease expiry: revoke failed",
							slog.Int64("lease_id", id),
							slog.Any("error", err))
					}
				}
			}
		}
	}
}
