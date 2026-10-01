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

package sqltest

import (
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// NewLockingHarness creates a Harness over a REAL in-memory store -- the
// MVCC engine with its lessor and watch hub, behind the same local backend a
// standalone node uses -- instead of a MemStore. Row locks are built from
// leases, watches and create-revision order, which a MemStore does not
// model; a test of row locking against one would test the double.
//
// The returned Harness's Store is nil. close releases the backend.
func NewLockingHarness() (h *Harness, closeFn func(), err error) {
	hub := watch.NewHub()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(),
		engine.WithMVCCLessor(lease.NewLessor()), engine.WithMVCCWatchHub(hub))
	if err != nil {
		return nil, nil, err
	}
	host := backend.NewLocalBackend(eng)

	kv := service.NewKVService(host, 1)
	mvcc := service.NewMVCCKVService(host, 1)
	txn := service.NewTransactionService(host, 1)
	seq := service.NewSequenceService(mvcc, txn)
	leaseSvc := service.NewLeaseService(host, 1)
	watchSvc := service.NewWatchService(hub)

	h = &Harness{
		TableSvc: service.NewTableService(kv, mvcc, txn, seq,
			service.WithReclaimIndex(txn, service.StorePlacementShared, "harness")),
		SeqSvc:   seq,
		PrivSvc:  service.NewPrivilegeService(kv, mvcc, txn),
		KVSvc:    kv,
		MVCCSvc:  mvcc,
		BatchSvc: service.NewBatchService(host, 1),
	}
	h.Registry = &FakeRegistry{
		TableServiceFn:       func(string, string) (service.TableServicer, error) { return h.TableSvc, nil },
		KVServiceFn:          func(string, string) (service.KVServicer, error) { return h.KVSvc, nil },
		MVCCServiceFn:        func(string, string) (service.MVCCKVServicer, error) { return h.MVCCSvc, nil },
		BatchServiceFn:       func(string, string) (service.BatchServicer, error) { return h.BatchSvc, nil },
		TransactionServiceFn: func(string, string) (service.TransactionServicer, error) { return txn, nil },
		SequenceServiceFn:    func(string, string) (service.SequenceServicer, error) { return h.SeqSvc, nil },
		PrivilegeServiceFn:   func(string, string) (service.PrivilegeServicer, error) { return h.PrivSvc, nil },
		CollectionServiceFn:  func(string, string) (service.CollectionServicer, error) { return h.CollectionSvc, nil },
		LeaseServiceFn:       func(string, string) (service.LeaseServicer, error) { return leaseSvc, nil },
		WatchServiceFn:       func(string, string) (service.WatchServicer, error) { return watchSvc, nil },
	}
	return h, func() { _ = host.Close() }, nil
}
