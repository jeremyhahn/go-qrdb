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

package replicaforward

import (
	"context"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// PlacementWatcher is the placement table's change feed (*placement.Store).
type PlacementWatcher interface {
	Watch(ctx context.Context) (<-chan placement.Event, error)
}

// MemberWatcher is the member records' change feed (cluster.MembershipStore).
type MemberWatcher interface {
	WatchMembers(ctx context.Context) (<-chan cluster.MemberEvent, error)
}

// resubscribeDelay paces a watch's re-subscription after its feed ends.
const resubscribeDelay = 500 * time.Millisecond

// RunInvalidation keeps cache fresh on this node until ctx ends: every
// placement change -- create, drop, move, rebalance, replica add or remove --
// evicts the database's entry, and every member change -- a new record, a
// changed endpoint, a lease that expired -- evicts the entries pointing at
// that member. A feed that ends is re-subscribed; while either feed is down
// the cache is cleared rather than trusted, since its entries can no longer
// be kept fresh. RunInvalidation blocks; run it on its own goroutine.
func RunInvalidation(ctx context.Context, cache *LeaderCache, placements PlacementWatcher, members MemberWatcher, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	done := make(chan struct{}, 2)
	go func() {
		watchLoop(ctx, cache, logger, "placement", func(ctx context.Context) (bool, error) {
			ch, err := placements.Watch(ctx)
			if err != nil {
				return false, err
			}
			for {
				select {
				case <-ctx.Done():
					return true, nil
				case ev, ok := <-ch:
					if !ok {
						return true, nil
					}
					cache.EvictDatabase(ev.TenantID, ev.Database)
				}
			}
		})
		done <- struct{}{}
	}()
	go func() {
		watchLoop(ctx, cache, logger, "member", func(ctx context.Context) (bool, error) {
			ch, err := members.WatchMembers(ctx)
			if err != nil {
				return false, err
			}
			for {
				select {
				case <-ctx.Done():
					return true, nil
				case ev, ok := <-ch:
					if !ok {
						return true, nil
					}
					cache.EvictMember(ev.Member.Address)
				}
			}
		})
		done <- struct{}{}
	}()
	<-done
	<-done
}

// watchLoop runs one feed until ctx ends, clearing the cache whenever the
// feed is not running.
func watchLoop(ctx context.Context, cache *LeaderCache, logger *slog.Logger, feed string, run func(context.Context) (bool, error)) {
	for ctx.Err() == nil {
		subscribed, err := run(ctx)
		cache.Clear()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Warn("replicaforward: invalidation feed down; leader cache cleared",
				slog.String("feed", feed), slog.Any("error", err))
		} else if subscribed {
			logger.Debug("replicaforward: invalidation feed ended; resubscribing", slog.String("feed", feed))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(resubscribeDelay):
		}
	}
}
