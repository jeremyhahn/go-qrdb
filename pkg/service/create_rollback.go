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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// createRollbackLedger records the creates refused by the strict-isolation
// tightening handshake whose rollback drop failed. Each such database stays
// fenced -- every read and write refused, its catalog record marked
// dropping -- and the quota reservations its create took stay held, so a
// fenced database never frees capacity it still occupies. The drop is
// retried by RetryCreateRollbacks, which releases the reservations once it
// lands. Each entry is bound to the refused database's ID and incarnation:
// a database created later under the same name is a different database and
// is never dropped by the retry.
type createRollbackLedger struct {
	mu      sync.Mutex
	pending map[createRollbackKey]*createRollbackEntry
}

type createRollbackKey struct {
	tenantID string
	name     string
}

// createRollbackEntry is the refused database's identity and the release of
// its reservations. claimed is set, under the ledger's lock, while one
// retry owns the entry: a concurrent retry skips it, so the drop is tried
// and the reservations released by one retry only.
type createRollbackEntry struct {
	id          string
	incarnation string
	release     func()
	claimed     bool
}

// holdCreateRollback records a failed strict-refusal rollback of the
// database db and the release of its reservations.
func (s *DatabaseService) holdCreateRollback(db *database.Database, release func()) {
	l := &s.createRollbacks
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == nil {
		l.pending = make(map[createRollbackKey]*createRollbackEntry)
	}
	l.pending[createRollbackKey{tenantID: db.TenantID(), name: db.Name()}] = &createRollbackEntry{
		id:          db.ID(),
		incarnation: db.Incarnation(),
		release:     release,
	}
}

// PendingCreateRollbacks reports how many strict-refused creates still wait
// for their rollback drop.
func (s *DatabaseService) PendingCreateRollbacks() int {
	l := &s.createRollbacks
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending)
}

// RetryCreateRollbacks retries the drop of every strict-refused create whose
// rollback drop failed, keeping the fence up on another failure, and
// releases a create's reservations once its drop lands (or the database is
// already gone). The drop is bound to the refused database's ID and
// incarnation: a name that now holds another database (the refused one was
// dropped and the name created again) is not the retry's to drop, and the
// entry is cleared without a drop. Each entry is claimed by one retry at a
// time, so concurrent retries never drop or release it twice. It returns
// the joined errors of the drops that failed again; those stay pending.
func (s *DatabaseService) RetryCreateRollbacks(ctx context.Context) error {
	l := &s.createRollbacks
	l.mu.Lock()
	keys := make([]createRollbackKey, 0, len(l.pending))
	for k := range l.pending {
		keys = append(keys, k)
	}
	l.mu.Unlock()

	var errs []error
	for _, k := range keys {
		entry := l.claim(k)
		if entry == nil {
			continue
		}
		dropCtx := database.KeepFenceOnFailure(tenant.WithTenantID(ctx, k.tenantID))
		if err := s.manager.DropIncarnationForTenant(dropCtx, k.name, entry.id, entry.incarnation); err != nil {
			var notFound *database.NotFoundError
			var superseded *database.DropSupersededError
			if !errors.As(err, &notFound) && !errors.As(err, &superseded) {
				l.unclaim(entry)
				errs = append(errs, err)
				continue
			}
		}
		l.settle(k, entry)
		if entry.release != nil {
			entry.release()
		}
	}
	return errors.Join(errs...)
}

// claim takes k's entry for one retry, or returns nil when it is gone or
// another retry holds it.
func (l *createRollbackLedger) claim(k createRollbackKey) *createRollbackEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.pending[k]
	if !ok || entry.claimed {
		return nil
	}
	entry.claimed = true
	return entry
}

// unclaim hands entry back after a failed drop, for the next retry.
func (l *createRollbackLedger) unclaim(entry *createRollbackEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.claimed = false
}

// settle removes entry from the ledger once its drop has landed (or its
// database is gone). An entry a later refusal recorded under k since is
// not this one and stays.
func (l *createRollbackLedger) settle(k createRollbackKey, entry *createRollbackEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending[k] == entry {
		delete(l.pending, k)
	}
}
