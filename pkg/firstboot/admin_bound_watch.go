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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// adminBoundWatchRetryCap bounds the exponential backoff WatchForAdminBound
// uses between retries of a lookup that fails for any reason other than
// "no admin account yet" -- the same cap VerifyAdminCertBindingWithin uses,
// so a degraded store delays this watch's start the same way it delays the
// boot-time check, never longer.
const adminBoundWatchRetryCap = time.Second

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
// It resolves the admin's user ID via lookup, then watches the EXACT key the
// real user store reads and writes the record under: a tenant-scoped store
// (every production composer's, qrdb's own and a higher-level IAM stack's
// adapter alike) namespaces every key by tenant.IdentityNamespace(tenantID)
// before it ever reaches the replicated KV plane, so the record this watch
// must see land is tenant.IdentityNamespace(tenantid.System) +
// user.PrimaryRecordKey(id), NOT the bare, un-namespaced PrimaryRecordKey a
// watch registered directly against the replicated hub would otherwise wait
// on forever.
//
// Because the lookup (step one) and the watch registration (step two) are
// two separate calls, a bind committed in between them would otherwise be
// missed: a watch started AFTER the bind would never see it as an event,
// and nothing else re-checks. So after the watch is registered, this
// re-reads the SAME record once more; if it already carries a live binding
// by then, notifier.AdminBound() fires immediately without waiting on an
// event at all. That the watch is registered before this re-check (not
// after) is what closes the gap: any bind that lands between the two reads
// either already committed by the time the re-check runs, or lands after
// the watch was already live to see it as an event -- there is no ordering
// left in which a bind is invisible to both.
//
// Every delivered event is decoded as a user.User and judged by the SAME
// verifyAdminBindings predicate the boot-time check applies; the first one
// that resolves to a live binding fires notifier.AdminBound() and the
// watch stops -- matching AwaitingAdminCredentialProbe.AdminBound's own
// "permanent for the life of this probe" contract, so a later expiry of
// that binding never re-arms the watch.
//
// A lookup that finds no admin account yet is not an error here: nothing
// to watch exists until first boot creates the account, and entry into
// "awaiting" is decided once, at boot, from that same account's state --
// not by this watch, which only ever narrows exit. WatchForAdminBound
// returns nil immediately in that case. A lookup that fails for any OTHER
// reason -- a degraded store, no leader yet -- retries with the same
// exponential backoff VerifyAdminCertBindingWithin uses (capped at
// adminBoundWatchRetryCap) rather than ending the watch: a transient fault
// at the moment this goroutine starts must not permanently strand the node
// in "awaiting" for lack of a watch that would otherwise have told it the
// node left that state.
//
// The caller runs this in its own goroutine; it blocks for the life of the
// watch (until ctx is canceled, the admin binds, or the watch itself
// fails) and is meant to be started only when the node's own
// AwaitingAdminCredentialProbe already reports awaiting at boot -- a node
// that is not awaiting has nothing this watch would change.
func WatchForAdminBound(ctx context.Context, lookup AdminLookup, watcher AdminRecordWatcher, notifier AdminBoundNotifier) error {
	u, err := lookupAdminRetrying(ctx, lookup)
	if err != nil {
		var unf *user.UserNotFoundError
		if errors.As(err, &unf) {
			return nil
		}
		return err
	}

	key := tenant.IdentityNamespace(tenantid.System) + user.PrimaryRecordKey(u.ID)
	w, err := watcher.Watch(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	// Re-check: close the gap between the lookup above and this watch's
	// own registration (see the doc comment).
	if u2, err := lookup.GetUserByUsername(ctx, tenantid.System, adminUsername); err == nil {
		if verifyAdminBindings(u2, time.Now().UTC()) == nil {
			notifier.AdminBound()
			return nil
		}
	}

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

// lookupAdminRetrying resolves the bootstrap admin's user record, retrying
// any failure other than "no admin account yet" with exponential backoff
// (50ms, doubling, capped at adminBoundWatchRetryCap) until ctx ends,
// rather than returning the first transient error and ending the watch
// that is meant to run for the life of the node.
func lookupAdminRetrying(ctx context.Context, lookup AdminLookup) (*user.User, error) {
	backoff := 50 * time.Millisecond
	for {
		u, err := lookup.GetUserByUsername(ctx, tenantid.System, adminUsername)
		if err == nil {
			return u, nil
		}
		var unf *user.UserNotFoundError
		if errors.As(err, &unf) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > adminBoundWatchRetryCap {
			backoff = adminBoundWatchRetryCap
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
