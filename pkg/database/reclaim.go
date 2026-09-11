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

package database

import "context"

// KeyspaceReclaimer deletes the data a database owns in a store it SHARES
// with other databases.
//
// The Manager's own teardown reclaims what the database holds on this node's
// filesystem: its configuration directory, its barrier metadata and, when the
// database has a store of its own, that store's files. For a database on the
// shared state machine — the default shape, and the only shape that may exist
// without a barrier — none of its data is there. It lives in the shared
// store under the key prefixes the (tenant, database) pair owns, and removing
// a directory does not touch it (go-qrdb#412). The Manager cannot reclaim it
// itself: the shared store is a replicated state machine several layers above
// pkg/database, which is why this is a seam and not a method.
//
// A collection's catalog entry and documents live in the shared store for
// EVERY database, including one with a dedicated store, because the
// collection surface is served by a single server-wide CollectionServicer
// over the system shard rather than by a per-(tenant, database) view. An
// implementation must therefore reclaim them unconditionally and must not
// short-circuit on "this database had its own store".
//
// Implementations MUST be idempotent. A database whose keyspace is already
// empty is the success case, not a not-found error: Drop reclaims the
// keyspace before it removes the catalog record precisely so that an
// interrupted drop can be completed by re-issuing it, and that retry runs the
// reclamation a second time over whatever the first one finished.
type KeyspaceReclaimer interface {
	// ReclaimDatabaseKeyspace deletes every key, index-catalog entry and
	// vector namespace the (tenantID, database) pair owns in the store
	// serving it. An empty tenantID means the system tenant, matching
	// tenant.ComposeNamespace.
	ReclaimDatabaseKeyspace(ctx context.Context, tenantID, database string) error
}

// WithKeyspaceReclaimer sets the KeyspaceReclaimer a Drop uses to reclaim a
// database's data from the shared store. Prefer SetKeyspaceReclaimer in the
// daemon: the Manager is constructed long before the data plane it needs.
func WithKeyspaceReclaimer(r KeyspaceReclaimer) ManagerOption {
	return func(m *Manager) {
		m.reclaimer = r
	}
}

// SetKeyspaceReclaimer sets or replaces the KeyspaceReclaimer used by Drop.
//
// It exists for the same ordering reason SetBarrierManager does: the Manager
// is built from configuration at the very start of boot, while the shared
// state machine that serves a database's keys — and the registry that knows
// which store serves which (tenant, database) pair — only exists much later.
// A daemon wires this in once the data plane is up.
//
// Thread-safety: call it before serving drop requests.
func (m *Manager) SetKeyspaceReclaimer(r KeyspaceReclaimer) {
	m.reclaimer = r
}

// KeyspaceReclaimer returns the configured reclaimer, or nil when none is
// wired. A nil reclaimer means Drop reclaims the filesystem only, which is
// correct for a Manager with no shared store behind it (a standalone
// single-database embedding) and a leak for one with (go-qrdb#412) — so the
// daemon asserts on it rather than discovering it later.
func (m *Manager) KeyspaceReclaimer() KeyspaceReclaimer {
	return m.reclaimer
}
