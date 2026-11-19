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
// lands.
type createRollbackLedger struct {
	mu      sync.Mutex
	pending map[createRollbackKey]func()
}

type createRollbackKey struct {
	tenantID string
	name     string
}

// holdCreateRollback records a failed strict-refusal rollback and the
// release of its reservations.
func (s *DatabaseService) holdCreateRollback(tenantID, name string, release func()) {
	l := &s.createRollbacks
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending == nil {
		l.pending = make(map[createRollbackKey]func())
	}
	l.pending[createRollbackKey{tenantID: tenantID, name: name}] = release
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
// already gone). It returns the joined errors of the drops that failed
// again; those stay pending.
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
		dropCtx := database.KeepFenceOnFailure(tenant.WithTenantID(ctx, k.tenantID))
		if err := s.manager.DropForTenant(dropCtx, k.name); err != nil {
			var notFound *database.NotFoundError
			if !errors.As(err, &notFound) {
				errs = append(errs, err)
				continue
			}
		}
		l.mu.Lock()
		release := l.pending[k]
		delete(l.pending, k)
		l.mu.Unlock()
		if release != nil {
			release()
		}
	}
	return errors.Join(errs...)
}
