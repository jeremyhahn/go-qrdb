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
