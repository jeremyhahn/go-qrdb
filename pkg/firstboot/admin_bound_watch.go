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

package firstboot

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// AdminRecordWatcher is the narrow Watch surface WatchForAdminBound needs:
// a single-key watch over the replicated store the bootstrap admin's user
// record lives in. *watch.Hub satisfies it directly; so does
// pkg/service.WatchServicer (the one a composer actually has in hand as
// svcNode.Watch), since its Watch method has the identical signature.
type AdminRecordWatcher interface {
	// Watch creates a watcher that delivers events for the specified key.
	Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// WatchForAdminBound watches the System tenant's bootstrap admin user
// record for the FIRST live certificate binding and calls
// notifier.AdminBound() the moment one appears (go-qrdb#560 review A).
//
// This is the FOLLOWER half of leaving "awaiting admin credential" without
// a restart. CeremonyServiceWithAdminRebind's own AdminBound call
// (pkg/service) only reaches the probe on the node that actually performed
// the reissue -- ordinarily the Raft leader, since ceremony-initialize's
// admin-credential retry is leader-gated. Every other node in the cluster
// learns the admin bound only once the write replicates, and the way to
// learn that WITHOUT re-reading the store on every request (the per-request
// probe AwaitingAdminCredentialProbe replaced, go-qrdb#560 review B1) is an
// event-driven watch on the replicated record itself.
//
// It resolves the admin's user ID once via lookup, then watches
// user.PrimaryRecordKey(id) for the life of ctx (or until the first live
// binding is observed). Every delivered event is decoded as a user.User and
// judged by the SAME verifyAdminBindings predicate the boot-time check
// applies; the first one that resolves to a live binding fires
// notifier.AdminBound() and the watch stops -- matching
// AwaitingAdminCredentialProbe.AdminBound's own "permanent for the life of
// this probe" contract, so a later expiry of that binding never re-arms
// the watch.
//
// A lookup that finds no admin account yet is not an error here: nothing
// to watch exists until first boot creates the account, and entry into
// "awaiting" is decided once, at boot, from that same account's state --
// not by this watch, which only ever narrows exit. WatchForAdminBound
// returns nil immediately in that case.
//
// The caller runs this in its own goroutine; it blocks for the life of the
// watch (until ctx is canceled, the admin binds, or the watch itself
// fails) and is meant to be started only when the node's own
// AwaitingAdminCredentialProbe already reports awaiting at boot -- a node
// that is not awaiting has nothing this watch would change.
func WatchForAdminBound(ctx context.Context, lookup AdminLookup, watcher AdminRecordWatcher, notifier AdminBoundNotifier) error {
	u, err := lookup.GetUserByUsername(ctx, tenantid.System, adminUsername)
	if err != nil {
		var unf *user.UserNotFoundError
		if errors.As(err, &unf) {
			return nil
		}
		return err
	}

	w, err := watcher.Watch(ctx, user.PrimaryRecordKey(u.ID))
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case resp, ok := <-w.Events():
			if !ok {
				return w.Err()
			}
			if bound := adminBoundFromEvents(resp.Events); bound {
				notifier.AdminBound()
				return nil
			}
		}
	}
}

// adminBoundFromEvents decodes each put event in evts as a user.User and
// reports whether any of them already holds a live certificate binding.
// Delete events and records that fail to decode are skipped -- the admin
// record is never deleted in ordinary operation, and a decode failure is
// not this watch's error to report when the next event may well decode
// cleanly.
func adminBoundFromEvents(evts []*watch.Event) bool {
	for _, evt := range evts {
		if evt == nil || evt.Kv == nil || evt.Type == watch.EventDelete {
			continue
		}
		var u user.User
		if err := json.Unmarshal(evt.Kv.Value, &u); err != nil {
			continue
		}
		if verifyAdminBindings(&u, time.Now().UTC()) == nil {
			return true
		}
	}
	return false
}
