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
)

// Members is the member-record surface forwarding reads and watches; a
// cluster.MembershipStore is one.
type Members interface {
	MemberSource
	MemberWatcher
}

// Start returns this node's Locator over the member records, with a fresh
// leader cache kept honest by placement and member watches (RunInvalidation)
// until ctx ends. It is how a node composes forwarding; selfRaftAddr is the
// node's Raft address.
//
// done closes once the watches have returned, after ctx ends, so the caller
// can wait for them before closing the services they read through.
func Start(ctx context.Context, members Members, placements PlacementWatcher, selfRaftAddr string, logger *slog.Logger) (locator *Locator, done <-chan struct{}) {
	cache := NewLeaderCache()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		RunInvalidation(ctx, cache, placements, members, logger)
	}()
	return NewLocator(members, cache, selfRaftAddr, logger), closed
}
