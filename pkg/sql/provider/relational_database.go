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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
// TableServicer, MVCCKVServicer, KVServicer, TransactionServicer,
// SequenceServicer, and BatchServicer are all resolved fresh on every call
// rather than cached on Database, because GetTableInsensitive is the first
// point in the request path that has a session -- and therefore a tenant --
// available; see the doc comment on WithRegistry for why resolving any
// earlier would risk binding the wrong tenant on a long-lived,
// connection-multiplexing Provider/Database. TransactionServicer backs
// every DML write's atomic row+index compare-and-swap, SequenceServicer
// backs AUTO_INCREMENT, and BatchServicer backs TRUNCATE (see
// relational_writer.go).
func (d *Database) relationalTable(ctx *sqle.Context, name string) (sqle.Table, bool, error) {
	tenantID := d.sessionTenantID(ctx)

	tableSvc, err := d.registry.TableService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	kvSvc, err := d.registry.KVService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}

	def, err := d.getTableResumingRewrite(ctx, tableSvc, kvSvc, name)
	if err != nil {
		var notFound *service.TableNotFoundError
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, err
	}

	mvccSvc, err := d.registry.MVCCService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	txnSvc, err := d.registry.TransactionService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	seqSvc, err := d.registry.SequenceService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}
	batchSvc, err := d.registry.BatchService(tenantID, d.name)
	if err != nil {
		return nil, false, err
	}

	tbl, err := NewRelationalTable(def, kvSvc, mvccSvc,
		WithTableService(tableSvc),
		WithTransactionService(txnSvc),
		WithSequenceService(seqSvc),
		WithBatchService(batchSvc),
	)
	if err != nil {
		return nil, false, err
	}
	return tbl, true, nil
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
func (d *Database) getTableResumingRewrite(ctx context.Context, tableSvc service.TableServicer, kvSvc service.KVServicer, name string) (*service.TableDef, error) {
	def, err := tableSvc.GetTable(ctx, d.name, name)
	var inProgress *service.TableRewriteInProgressError
	if !errors.As(err, &inProgress) {
		return def, err
	}

	if rerr := resumeRewrite(ctx, tableSvc, kvSvc, d.name, name); rerr != nil {
		return nil, rerr
	}
	return tableSvc.GetTable(ctx, d.name, name)
}
