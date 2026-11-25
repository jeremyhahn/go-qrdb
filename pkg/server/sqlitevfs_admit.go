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

package server

import (
	"bytes"
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// sqliteAdmission holds the checkers and the usage recorder the SQLite VFS
// creation gate (row #926, o1) uses for every tenant: ents for the
// quota.FeatureSQLiteVFS entitlement, lim for the
// quota.ResourceSQLiteDatabases cap, usage for that resource's count.
type sqliteAdmission struct {
	ents  quota.EntitlementChecker
	lim   quota.Checker
	usage service.QuotaUsageRecorder
}

// newSQLiteAdmission builds the gate's checkers over the tenant service: its
// entitlements, a quota.Service over its quotas whose sqlite_databases
// counter reads the persisted usage, and its usage recorder. nil when
// tenantSvc is not all three.
func newSQLiteAdmission(tenantSvc tenant.TenantServicer) *sqliteAdmission {
	if tenantSvc == nil {
		return nil
	}
	ents, ok := tenantSvc.(quota.EntitlementChecker)
	if !ok {
		return nil
	}
	provider, ok := tenantSvc.(quota.QuotaProvider)
	if !ok {
		return nil
	}
	usage, ok := tenantSvc.(service.QuotaUsageRecorder)
	if !ok {
		return nil
	}
	lim, err := quota.NewService(provider, nil)
	if err != nil {
		return nil
	}
	lim.RegisterCounter(quota.ResourceSQLiteDatabases, func(ctx context.Context, tenantID string) (int64, error) {
		return tenantSvc.GetQuotaUsage(ctx, tenantID, quota.ResourceSQLiteDatabases)
	})
	return &sqliteAdmission{ents: ents, lim: lim, usage: usage}
}

// sqliteAdmitGate is the creation gate of one tenant's scope. The page store
// lives in the application (docs/sqlite-vfs/architecture.md), so the server
// never constructs a Store for a caller; it gates the write that creates a
// database where it lands: a commit (TransactionServicer.Execute) or a
// staged publish (MovePrefix) that puts sqlite/<db>/meta while it has no
// live value is admitted by sqlitevfs.Admit first, and a refusal writes
// nothing. Once applied, the create counts +1 in the tenant's
// sqlite_databases usage and a delete of a live meta record (a dropped
// database) counts -1. The existence read and the write are not atomic, so
// the count is the soft bound sqlitevfs.Admit documents.
type sqliteAdmitGate struct {
	adm      *sqliteAdmission
	tenantID string
	backend  sqlitevfs.Backend
}

// metaDatabase returns the database whose meta record key is, if it is one.
func metaDatabase(key []byte) (string, bool) {
	db, ok := sqlitevfs.DatabaseOfKey(key)
	if !ok {
		return "", false
	}
	metaKey, err := sqlitevfs.MetaKey(db)
	if err != nil || !bytes.Equal(metaKey, key) {
		return "", false
	}
	return db, true
}

// admit admits every meta put of opSets and returns, for each database whose
// meta record any op of opSets names, whether it was live before.
func (g *sqliteAdmitGate) admit(ctx context.Context, opSets ...[]service.Op) (map[string]bool, error) {
	var live map[string]bool
	for _, ops := range opSets {
		for _, op := range ops {
			if op.Type != service.OpPut && op.Type != service.OpDelete {
				continue
			}
			db, ok := metaDatabase(op.Key)
			if !ok {
				continue
			}
			if live == nil {
				live = make(map[string]bool)
			}
			if _, known := live[db]; !known {
				found, err := g.metaLive(ctx, op.Key)
				if err != nil {
					return nil, err
				}
				live[db] = found
			}
			if op.Type == service.OpPut && !live[db] {
				if err := sqlitevfs.Admit(ctx, g.backend, g.tenantID, db, g.adm.ents, g.adm.lim); err != nil {
					return nil, err
				}
			}
		}
	}
	return live, nil
}

// metaLive reports whether key has a live value at the newest revision.
func (g *sqliteAdmitGate) metaLive(ctx context.Context, key []byte) (bool, error) {
	rev, err := g.backend.Revision(ctx)
	if err != nil {
		return false, err
	}
	_, found, err := g.backend.GetAt(ctx, key, rev)
	return found, err
}

// record counts the creates and drops of the applied ops in the tenant's
// sqlite_databases usage. The system tenant records nothing. A recorder
// error is not returned: the write is committed, and the count is soft.
func (g *sqliteAdmitGate) record(ctx context.Context, live map[string]bool, applied []service.Op) {
	if len(live) == 0 || g.adm.usage == nil || tenantid.IsSystem(g.tenantID) {
		return
	}
	for _, op := range applied {
		db, ok := metaDatabase(op.Key)
		if !ok {
			continue
		}
		switch {
		case op.Type == service.OpPut && !live[db]:
			live[db] = true
			_, _ = g.adm.usage.IncrementQuotaUsage(ctx, g.tenantID, quota.ResourceSQLiteDatabases, 1)
		case op.Type == service.OpDelete && len(op.RangeEnd) == 0 && live[db]:
			live[db] = false
			_, _ = g.adm.usage.DecrementQuotaUsage(ctx, g.tenantID, quota.ResourceSQLiteDatabases, 1)
		}
	}
}

// sqliteAdmitTxn is the transaction service of a gated scope.
type sqliteAdmitTxn struct {
	service.TransactionServicer
	gate *sqliteAdmitGate
}

// Execute gates the creating write of a SQLite database, then forwards.
func (s *sqliteAdmitTxn) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	live, err := s.gate.admit(ctx, successOps, failureOps)
	if err != nil {
		return nil, err
	}
	resp, err := s.TransactionServicer.Execute(ctx, compares, successOps, failureOps)
	s.recordResponse(ctx, live, resp, err, successOps, failureOps)
	return resp, err
}

// ExecuteWithMode is Execute with a write durability.
func (s *sqliteAdmitTxn) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
	live, err := s.gate.admit(ctx, successOps, failureOps)
	if err != nil {
		return nil, err
	}
	resp, err := s.TransactionServicer.ExecuteWithMode(ctx, compares, successOps, failureOps, mode)
	s.recordResponse(ctx, live, resp, err, successOps, failureOps)
	return resp, err
}

func (s *sqliteAdmitTxn) recordResponse(ctx context.Context, live map[string]bool, resp *service.TransactionResponse, err error, successOps, failureOps []service.Op) {
	if err != nil || resp == nil {
		return
	}
	if resp.Succeeded {
		s.gate.record(ctx, live, successOps)
		return
	}
	s.gate.record(ctx, live, failureOps)
}

// sqliteGatedKV is a ServiceKV whose prefix moves pass the admission gate.
type sqliteGatedKV struct {
	sqlitevfs.ServiceKV
	mover service.PrefixMover
}

// MovePrefix moves through the gated mover.
func (k sqliteGatedKV) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	return k.mover.MovePrefix(ctx, req)
}

// sqliteGatedServiceTxn is a ServiceTxn that admits and counts the SQLite
// databases its transactions create or delete.
type sqliteGatedServiceTxn struct {
	inner sqlitevfs.ServiceTxn
	gate  *sqliteAdmitGate
}

// Execute admits the transaction's meta-record writes, runs it and records
// the databases the applied branch created or deleted.
func (s sqliteGatedServiceTxn) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	live, err := s.gate.admit(ctx, successOps, failureOps)
	if err != nil {
		return nil, err
	}
	resp, err := s.inner.Execute(ctx, compares, successOps, failureOps)
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.Succeeded {
		s.gate.record(ctx, live, successOps)
	} else {
		s.gate.record(ctx, live, failureOps)
	}
	return resp, nil
}

// withSQLiteAdmission returns svcs with the SQLite creation gate on its
// transaction service and its MVCC MovePrefix, and its KV and batch services
// refusing SQLite keys. svcs is returned as it is when adm is nil; the gate is
// left off when it is already on or its services cannot back a page store
// (no SQLite restore hook, see withSQLiteRestoreHook).
func withSQLiteAdmission(svcs *DatabaseServices, tenantID string, adm *sqliteAdmission) *DatabaseServices {
	if adm == nil || svcs == nil {
		return svcs
	}
	svcs = withSQLiteKeyGuard(svcs)
	if svcs.Transaction == nil {
		return svcs
	}
	mvcc, ok := svcs.MVCC.(*sqliteRestoreMVCC)
	if !ok || mvcc.gate != nil {
		return svcs
	}
	backend, err := sqlitevfs.NewServiceBackend(mvcc.kv, mvcc.txn)
	if err != nil {
		return svcs
	}
	gate := &sqliteAdmitGate{adm: adm, tenantID: tenantID, backend: backend}
	gated := *mvcc
	gated.gate = gate
	out := *svcs
	out.MVCC = &gated
	out.Transaction = &sqliteAdmitTxn{TransactionServicer: svcs.Transaction, gate: gate}
	return &out
}

// withSQLiteKeyGuard returns svcs with its KV and batch services refusing
// writes to the keys of a SQLite database, which only the gated
// transaction service writes.
func withSQLiteKeyGuard(svcs *DatabaseServices) *DatabaseServices {
	out := *svcs
	if _, guarded := svcs.KV.(sqliteKeyGuardKV); svcs.KV != nil && !guarded {
		out.KV = sqliteKeyGuardKV{KVServicer: svcs.KV}
	}
	if _, guarded := svcs.Batch.(sqliteKeyGuardBatch); svcs.Batch != nil && !guarded {
		out.Batch = sqliteKeyGuardBatch{BatchServicer: svcs.Batch}
	}
	return &out
}

// sqliteKeyOwned refuses key when it belongs to a SQLite database.
func sqliteKeyOwned(key []byte) error {
	if _, ok := sqlitevfs.DatabaseOfKey(key); ok {
		return &service.SQLiteKeyError{Key: string(key)}
	}
	return nil
}

// sqliteKeyGuardKV is a KVServicer that refuses writes to SQLite keys.
type sqliteKeyGuardKV struct {
	service.KVServicer
}

func (k sqliteKeyGuardKV) Put(ctx context.Context, key string, value []byte) error {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.Put(ctx, key, value)
}

func (k sqliteKeyGuardKV) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.PutWithMode(ctx, key, value, mode)
}

func (k sqliteKeyGuardKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return false, err
	}
	return k.KVServicer.PutIfAbsent(ctx, key, value)
}

func (k sqliteKeyGuardKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return false, err
	}
	return k.KVServicer.CompareAndSwap(ctx, key, expected, next)
}

func (k sqliteKeyGuardKV) Delete(ctx context.Context, key string) error {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.Delete(ctx, key)
}

func (k sqliteKeyGuardKV) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.DeleteWithMode(ctx, key, mode)
}

func (k sqliteKeyGuardKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if err := sqliteKeyOwned([]byte(key)); err != nil {
		return false, err
	}
	return k.KVServicer.CompareAndDelete(ctx, key, expected)
}

// sqliteKeyGuardBatch is a BatchServicer that refuses batches writing SQLite
// keys.
type sqliteKeyGuardBatch struct {
	service.BatchServicer
}

func sqliteBatchOwned(ops []service.Op) error {
	for _, op := range ops {
		if op.Type != service.OpPut && op.Type != service.OpDelete {
			continue
		}
		if err := sqliteKeyOwned(op.Key); err != nil {
			return err
		}
	}
	return nil
}

func (b sqliteKeyGuardBatch) Execute(ctx context.Context, ops []service.Op) (*service.BatchResponse, error) {
	if err := sqliteBatchOwned(ops); err != nil {
		return nil, err
	}
	return b.BatchServicer.Execute(ctx, ops)
}

func (b sqliteKeyGuardBatch) ExecuteWithMode(ctx context.Context, ops []service.Op, mode writemode.Mode) (*service.BatchResponse, error) {
	if err := sqliteBatchOwned(ops); err != nil {
		return nil, err
	}
	return b.BatchServicer.ExecuteWithMode(ctx, ops, mode)
}
