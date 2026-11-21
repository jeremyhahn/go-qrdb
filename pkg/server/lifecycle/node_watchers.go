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

package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// StartNodeWatchers starts every watcher a serving node runs for its whole
// life, so qrdbd and sdk/go/node compose the same set from one place:
//
//   - Key B cache eviction (WatchCertProviderEvictions), so a retired
//     tenant's CA stops being cached and trusted promptly rather than aging
//     out by TTL alone;
//   - undone-tenant acknowledgement (startUndoneWatch), so a create of an
//     undone id is not held until this node's next deleted-tenant reconcile.
//
// The tenant home roster watch is not started here: it changes Raft
// configuration, so it starts only once the raft host is composed
// (StartTenantHomeWatch, called by ComposeRaftTenantCluster right after
// SetRaftHost).
//
// Call once, after BuildServices, with the node's long-lived background
// context. n.Stop ends every watcher this starts and waits for it, before it
// closes anything the watcher reads through. A watcher that fails to start
// does not stop the others; their
// errors are joined. Neither is fatal to the node: an undone id still clears
// at the next reconcile-deleted-tenants pass, and Key B still ages out.
func StartNodeWatchers(ctx context.Context, n *Node) error {
	return errors.Join(
		WatchCertProviderEvictions(ctx, n),
		startUndoneWatch(ctx, n),
	)
}

// tenantHomeWatcher is the database service's committed-roster watch
// (service.DatabaseService.WatchTenantHomes).
type tenantHomeWatcher interface {
	WatchTenantHomes(ctx context.Context, tick <-chan time.Time)
}

// StartTenantHomeWatch runs the tenant home roster watch for the node's
// lifetime, whether or not dynamic Raft membership is enabled: a node added
// or removed by an explicit membership change must join or leave every
// tenant home too, not only one the dynamic membership manager admits.
// It is started only after the database service's raft host is set
// (ComposeRaftTenantCluster), so its first reconcile can check leadership
// and change the group's Raft configuration; qrdbd and sdk/go/node both
// reach it through that one composition.
func StartTenantHomeWatch(n *Node) {
	if n == nil || n.Database == nil {
		return
	}
	w, ok := n.Database.(tenantHomeWatcher)
	if !ok {
		return
	}
	n.work.start(func(ctx context.Context) {
		t := time.NewTicker(service.TenantHomeWatchInterval)
		defer t.Stop()
		w.WatchTenantHomes(ctx, t.C)
	})
}
