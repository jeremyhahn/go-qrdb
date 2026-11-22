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

// The sqlite-vfs page store against the tenant's storage quota (row #926,
// slice 9, Q5): over the services the server resolves with a real
// quota.Accountant, a commit or a staged publish that would take tenant A
// past its MaxStorageBytes fails with sqlitevfs.ErrFull (wrapping the
// *quota.ExceededError), leaves the last good commit whole, cleans up its
// stage and releases the writer lock, on the prefix, volume and raft
// isolation types. Tenant B, with no quota, keeps committing.

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// quotaTenantServicer answers GetTenant as isolationTenantServicer does and
// gives lockTenantA a storage quota of limit bytes, read on every resolve;
// lockTenantB has no quota.
type quotaTenantServicer struct {
	isolationTenantServicer
	limit atomic.Int64
}

func (s *quotaTenantServicer) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	info, err := s.isolationTenantServicer.GetTenant(ctx, tenantID)
	if err != nil || tenantID != lockTenantA {
		return info, err
	}
	info.Quotas = &quota.Quotas{MaxStorageBytes: s.limit.Load()}
	return info, nil
}

// quotaScene is one isolation type's registry with a real accountant.
type quotaScene struct {
	registry service.Registry
	ts       *quotaTenantServicer
	acct     *quota.Accountant
}

func newQuotaScene(t *testing.T, scene lockSceneWith) *quotaScene {
	t.Helper()
	ts := &quotaTenantServicer{isolationTenantServicer: isolationTenantServicer{isolation: scene.isolation}}
	ts.limit.Store(math.MaxInt64)
	acct, err := quota.NewAccountant(quota.AccountantConfig{
		Quotas: func(ctx context.Context, id string) (*quota.Quotas, error) {
			info, err := ts.GetTenant(ctx, id)
			if err != nil {
				return nil, err
			}
			return info.Quotas, nil
		},
		Flush: func(context.Context, string, int64) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	return &quotaScene{registry: scene.build(t, ts, withQuotaAccountant(acct)).registry, ts: ts, acct: acct}
}

// store builds a fresh Store for tenantID as holder, so the registry
// resolves the services again and reads the quota's current limit. A staged
// commit writes one page per stage chunk.
func (q *quotaScene) store(t *testing.T, tenantID, holder string) *wiringStore {
	t.Helper()
	mvccSvc, err := q.registry.MVCCService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("MVCCService(%s): %v", tenantID, err)
	}
	txnSvc, err := q.registry.TransactionService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("TransactionService(%s): %v", tenantID, err)
	}
	kv, ok := mvccSvc.(sqlitevfs.ServiceKV)
	if !ok {
		t.Fatalf("MVCC servicer %T is not a sqlitevfs.ServiceKV", mvccSvc)
	}
	txn, ok := txnSvc.(sqlitevfs.ServiceTxn)
	if !ok {
		t.Fatalf("transaction servicer %T is not a sqlitevfs.ServiceTxn", txnSvc)
	}
	backend, err := sqlitevfs.NewServiceBackend(kv, txn)
	if err != nil {
		t.Fatalf("NewServiceBackend: %v", err)
	}
	w, err := sqlitevfs.NewLockServiceWriter(lockSvcFor(t, q.registry, tenantID), holder, lockTTL)
	if err != nil {
		t.Fatalf("NewLockServiceWriter: %v", err)
	}
	s, err := sqlitevfs.NewStore(backend, wiringDB,
		sqlitevfs.WithWriterLock(w),
		sqlitevfs.WithStageThreshold(wiringStageThreshold),
		sqlitevfs.WithStageChunkBytes(wiringPageSize))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &wiringStore{store: s, backend: backend}
}

// quotaGoodPages is tenant A's last good commit: a staged 3-page commit.
var quotaGoodPages = []byte{0x71, 0x72, 0x73}

// fillWithinLimit commits a plain 1-page and then a staged 3-page commit for
// tenant A under a limit they fit in, and reads them back.
func (q *quotaScene) fillWithinLimit(t *testing.T) {
	t.Helper()
	a := q.store(t, lockTenantA, lockHolderA)
	a.commitPages(t, 0x70)
	a.assertPages(t, 0x70)
	a.commitPages(t, quotaGoodPages...)
	a.assertPages(t, quotaGoodPages...)
	if q.acct.Usage(lockTenantA) <= 0 {
		t.Fatalf("usage of %s after two commits = %d, want it charged", lockTenantA, q.acct.Usage(lockTenantA))
	}
}

// commitFull commits fills for tenant A and wants ErrFull wrapping A's
// *quota.ExceededError.
func commitFull(t *testing.T, a *wiringStore, fills ...byte) {
	t.Helper()
	txn := a.reserved(t)
	wiringWrite(t, txn, fills...)
	_, err := txn.Commit(t.Context())
	if err == nil {
		t.Fatalf("commit of %d pages past the quota succeeded, want ErrFull", len(fills))
	}
	if !errors.Is(err, sqlitevfs.ErrFull) {
		t.Fatalf("commit past the quota err = %v, want ErrFull", err)
	}
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) {
		t.Fatalf("commit past the quota err = %v, want it to wrap *quota.ExceededError", err)
	}
	if exceeded.TenantID != lockTenantA {
		t.Fatalf("ExceededError names tenant %q, want %q", exceeded.TenantID, lockTenantA)
	}
}

// assertWriterFree wants another holder of tenant A to take the writer lock.
func (q *quotaScene) assertWriterFree(t *testing.T) {
	t.Helper()
	other := q.store(t, lockTenantA, lockHolderB)
	txn := other.reserved(t)
	if err := txn.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestSQLiteVFSQuota_CommitsWithinLimit(t *testing.T) {
	for name, scene := range lockScenesWith {
		t.Run(name, func(t *testing.T) {
			q := newQuotaScene(t, scene)
			q.fillWithinLimit(t)
		})
	}
}

func TestSQLiteVFSQuota_PlainCommitPastLimitIsErrFull(t *testing.T) {
	for name, scene := range lockScenesWith {
		t.Run(name, func(t *testing.T) {
			q := newQuotaScene(t, scene)
			q.fillWithinLimit(t)
			// One page fits, its commit's meta record does not.
			q.ts.limit.Store(q.acct.Usage(lockTenantA) + wiringPageSize)
			a := q.store(t, lockTenantA, lockHolderA)
			commitFull(t, a, 0x7f)
			a.assertPages(t, quotaGoodPages...)
			q.assertWriterFree(t)
		})
	}
}

func TestSQLiteVFSQuota_StagedCommitPastLimitIsErrFull(t *testing.T) {
	for name, scene := range lockScenesWith {
		t.Run(name, func(t *testing.T) {
			q := newQuotaScene(t, scene)
			q.fillWithinLimit(t)
			// The first one-page stage chunk fits, the second crosses.
			q.ts.limit.Store(q.acct.Usage(lockTenantA) + wiringPageSize + wiringPageSize/2)
			a := q.store(t, lockTenantA, lockHolderA)
			commitFull(t, a, 0x81, 0x82, 0x83)

			now, err := a.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			stageRoot, err := sqlitevfs.StageRoot(wiringDB)
			if err != nil {
				t.Fatalf("StageRoot: %v", err)
			}
			if live, err := a.backend.Keys(t.Context(), stageRoot, now); err != nil || len(live) != 0 {
				t.Fatalf("live stage keys after a refused staged commit = %q, %v, want none", live, err)
			}
			a.assertPages(t, quotaGoodPages...)
			q.assertWriterFree(t)
		})
	}
}

func TestSQLiteVFSQuota_OtherTenantCommitsWhileFull(t *testing.T) {
	for name, scene := range lockScenesWith {
		t.Run(name, func(t *testing.T) {
			q := newQuotaScene(t, scene)
			q.fillWithinLimit(t)
			q.ts.limit.Store(q.acct.Usage(lockTenantA) + wiringPageSize)
			a := q.store(t, lockTenantA, lockHolderA)
			commitFull(t, a, 0x7f)

			b := q.store(t, lockTenantB, lockHolderB)
			b.commitPages(t, quotaGoodPages...)
			b.assertPages(t, quotaGoodPages...)
			a.assertPages(t, quotaGoodPages...)
		})
	}
}
