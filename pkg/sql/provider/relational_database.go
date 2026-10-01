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

package provider

import (
	"context"
	"errors"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/rowlock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// relationalTable resolves name as a user-defined relational table created
// through TableServicer, scoped to the SQL session's tenant (see
// sessionTenantID) and this database's name. found is false -- not an error
// -- when name does not name a relational table, so GetTableInsensitive can
// fall through to the next resolution step (dynamic collections) exactly as
// a missing entry at any other resolution step does. Any other error is
// returned to the caller rather than swallowed.
//
// A *service.TableRewriteInProgressError from the underlying GetTable is
// handled specially rather than simply propagated: see
// getTableResumingRewrite.
//
// TableServicer, MVCCKVServicer, KVServicer, TransactionServicer, and
// SequenceServicer are all resolved fresh on every call rather than cached
// on Database, because GetTableInsensitive is the first point in the
// request path that has a session -- and therefore a tenant -- available;
// see the doc comment on WithRegistry for why resolving any earlier would
// risk binding the wrong tenant on a long-lived, connection-multiplexing
// Provider/Database. TransactionServicer backs every DML write's atomic
// row+index compare-and-swap, and SequenceServicer backs AUTO_INCREMENT.
// TRUNCATE goes through TableServicer.TruncateTable instead of a KV-level
// scan (see relational_writer.go's Truncate), so it needs no BatchServicer.
func (d *Database) relationalTable(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	tenantID := d.sessionTenantID(ctx)
	registry := d.registryFor(ctx)

	tableSvc, err := registry.TableService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	kvSvc, err := registry.KVService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	// Resolved here, ahead of getTableResumingRewrite, because a resumed
	// rewrite's own write-back (runRewrite, relational_rewrite.go) commits
	// through a compare-and-swap fenced on the table's generation, not a
	// plain KV Put -- see go-qrdb#547.
	txnSvc, err := registry.TransactionService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}

	// Bound to the tenant: completing an interrupted rewrite may publish a
	// ReclaimJob for the index incarnations it retires.
	def, err := tableDef(ctx, tenantID, d.name, name, func() (*service.TableDef, error) {
		return d.getTableResumingRewrite(tenant.WithTenantID(ctx, tenantID), tableSvc, kvSvc, txnSvc, name)
	})
	if err != nil {
		var notFound *service.TableNotFoundError
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		// A table created by v0.20.0 through v0.21.0 resolves -- so DROP
		// TABLE can remove it -- but refuses every read and write.
		var legacy *service.TableLayoutUnsupportedError
		if errors.As(err, &legacy) {
			return newLegacyLayoutTable(name, tableSvc, legacy), true, nil
		}
		return nil, false, err
	}

	mvccSvc, err := registry.MVCCService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	seqSvc, err := registry.SequenceService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}

	opts := []RelationalTableOption{
		WithTableService(tableSvc),
		WithTransactionService(txnSvc),
		WithSequenceService(seqSvc),
		WithRowLocks(func() (*rowlock.Table, error) {
			return d.rowLockTable(registry, tenantID, txnSvc, mvccSvc)
		}, d.concurrency(tenantID)),
	}
	if d.postgres {
		opts = append(opts, WithPostgreSQLDialect())
	}
	if d.estimates != nil {
		opts = append(opts, withRowEstimates(d.estimates, estimateStore{tenant: tenantID, db: d.name}))
	}

	tbl, err := NewRelationalTable(def, kvSvc, mvccSvc, opts...)
	if err != nil {
		return nil, false, err
	}
	return tbl, true, nil
}

// rowLockTable builds the lock table of this database for tenantID's
// session: the same (tenant, database)-scoped store the rows live in, so the
// lock records are replicated with them and confined to the tenant exactly
// as the rows are. A registry that cannot resolve a lease or watch service
// leaves the table without one; the error is reported only to a statement
// that needs a lock.
func (d *Database) rowLockTable(registry service.Registry, tenantID string, txnSvc service.TransactionServicer, mvccSvc service.MVCCKVServicer) (*rowlock.Table, error) {
	leaseSvc, err := registry.LeaseService(tenantID, d.name)
	if err != nil {
		return nil, err
	}
	watchSvc, err := registry.WatchService(tenantID, d.name)
	if err != nil {
		return nil, err
	}
	return rowlock.New(rowlock.Deps{Txn: txnSvc, MVCC: mvccSvc, Lease: leaseSvc, Watch: watchSvc}, tenantID+"/"+d.name)
}

// concurrency returns this database's configured SQL concurrency mode.
func (d *Database) concurrency(tenantID string) database.Concurrency {
	if d.sqlConfigOf == nil {
		return database.ConcurrencyOptimistic
	}
	return d.sqlConfigOf(tenantID, d.name).EffectiveConcurrency()
}

// getTableResumingRewrite calls tableSvc.GetTable and, when it reports
// *service.TableRewriteInProgressError -- an earlier DROP/MODIFY COLUMN's
// row migration was interrupted (most likely by a crash) before it could
// complete -- attempts to finish that migration itself (resumeRewrite)
// before retrying GetTable exactly once more. This is what makes an
// interrupted rewrite self-heal on the very next access to the table,
// rather than leaving it permanently stuck refusing to serve.
//
// A failure while resuming (as opposed to "nothing to resume", which
// resumeRewrite reports as success) is returned as-is: the caller has no
// better option than surfacing it, since the table cannot be served with
// rows in a mixed encoding regardless.
func (d *Database) getTableResumingRewrite(ctx context.Context, tableSvc service.TableServicer, kvSvc service.KVServicer, txnSvc service.TransactionServicer, name string) (*service.TableDef, error) {
	def, err := tableSvc.GetTable(ctx, d.name, name)
	var inProgress *service.TableRewriteInProgressError
	if !errors.As(err, &inProgress) {
		return def, err
	}

	if rerr := resumeRewrite(ctx, tableSvc, kvSvc, txnSvc, d.name, name); rerr != nil {
		return nil, rerr
	}
	return tableSvc.GetTable(ctx, d.name, name)
}
