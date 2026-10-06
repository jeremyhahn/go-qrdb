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
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// Quota enforcement coverage across write-bearing engines
//
// applyQuota wraps the engines that commit independent tenant payload bytes to
// the shared store outside the KV/MVCC servicers:
//
//   - KV / MVCC      — wrapped (quota_kv.go, quota_mvcc.go).
//   - Batch          — wrapped (this file): Execute/ExecuteWithMode/ExecuteEntity
//                      commit KV/MVCC Puts directly to the host.
//   - Transaction    — wrapped (quota_transaction.go): same Put surface.
//   - TimeSeries     — wrapped (quota_timeseries.go): Write/WriteBatch/WritePoint
//                      ingest data points whose size is len(metric) +
//                      value+timestamp + tag bytes.
//   - Vector         — wrapped (quota_vector.go): UpsertVector/BatchUpsert/
//                      UpdateMetadata write embeddings + metadata, sized as
//                      len(vector)*4 (float32) + metadata key/value bytes.
//
// The Index engine is intentionally NOT wrapped (N/A): index writes never carry
// an independent tenant payload — a secondary index is an engine-maintained
// projection of KV/MVCC data whose underlying value bytes are already accounted
// at the KV/MVCC write that produced them. The IndexServicer surface is
// create/drop/query, not value storage, so wrapping it would double-count.

// quotaBatchService wraps a BatchServicer to enforce a tenant's storage-byte
// quota. The total byte length of the put values in a batch is admitted through
// the Accountant before the batch is proposed; a batch that would push the
// tenant over its MaxStorageBytes is rejected with *quota.ExceededError before
// it reaches consensus. Reserved bytes are released if the batch fails.
//
// Consistent with the KV quota path, puts are accounted by value length (an
// over-count for overwrites that the reconciler corrects for volume tenants),
// and deletes are not credited here — computing the freed bytes would require a
// prior read per key on the batch hot path.
type quotaBatchService struct {
	inner    service.BatchServicer
	acct     *quota.Accountant
	tenantID string
	limit    int64
}

// Compile-time interface check.
var _ service.BatchServicer = (*quotaBatchService)(nil)

// newQuotaBatchService wraps inner with storage-byte quota enforcement for the
// given tenant and limit (MaxStorageBytes).
func newQuotaBatchService(inner service.BatchServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaBatchService {
	return &quotaBatchService{inner: inner, acct: acct, tenantID: tenantID, limit: limit}
}

// reserve admits n bytes against the tenant's quota.
func (s *quotaBatchService) reserve(ctx context.Context, n int) error {
	return s.acct.Reserve(ctx, s.tenantID, s.limit, int64(n))
}

// opPutBytes sums the value lengths of the put operations in ops.
func opPutBytes(ops []service.Op) int {
	var n int
	for _, op := range ops {
		if op.Type == service.OpPut {
			n += len(op.Value)
		}
	}
	return n
}

// entityPutBytes sums the data lengths of the put operations in ops.
func entityPutBytes(ops []service.BatchEntityOp) int {
	var n int
	for _, op := range ops {
		if op.Type == "put" {
			n += len(op.Data)
		}
	}
	return n
}

// Execute admits the batch's put bytes against the quota, then forwards.
func (s *quotaBatchService) Execute(ctx context.Context, ops []service.Op) ([]service.OpResponse, error) {
	n := opPutBytes(ops)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.Execute(ctx, ops)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// ExecuteWithMode admits the batch's put bytes against the quota, then forwards.
func (s *quotaBatchService) ExecuteWithMode(ctx context.Context, ops []service.Op, mode writemode.Mode) ([]service.OpResponse, error) {
	n := opPutBytes(ops)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.ExecuteWithMode(ctx, ops, mode)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// ExecuteEntity admits the entity batch's put bytes against the quota, then
// forwards.
func (s *quotaBatchService) ExecuteEntity(ctx context.Context, req *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	var n int
	if req != nil {
		n = entityPutBytes(req.Ops)
	}
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.ExecuteEntity(ctx, req)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// ImportBegin, ImportAppend, ImportCommit and ImportAbort are forwarded
// unchanged. The namespaced wrapper already rejects imports into a
// non-default scope, and unscoped imports run on the unmetered system
// tenant where no quota wrapper is installed. A session's own quotas
// (pkg/config.ImportSessionConfig: per-session bytes and chunk count,
// per-tenant concurrent sessions) are a different, narrower resource than
// this wrapper's tenant storage-byte quota, and are enforced inside
// BatchService itself rather than by a wrapper here.
func (s *quotaBatchService) ImportBegin(ctx context.Context, opts service.ImportOptions) (string, error) {
	return s.inner.ImportBegin(ctx, opts)
}

func (s *quotaBatchService) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	return s.inner.ImportAppend(ctx, sessionID, seq, chunk)
}

func (s *quotaBatchService) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	return s.inner.ImportCommit(ctx, sessionID)
}

func (s *quotaBatchService) ImportAbort(ctx context.Context, sessionID string) error {
	return s.inner.ImportAbort(ctx, sessionID)
}
