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

// quotaTransactionService wraps a TransactionServicer to enforce a tenant's
// storage-byte quota. A transaction commits KV/MVCC Puts directly to the host,
// bypassing the wrapped KV/MVCC servicers, so its put bytes are admitted through
// the Accountant here. Because either the success or the failure branch runs
// (never both), the reservation uses the larger of the two branches' put bytes,
// guaranteeing the admitted amount covers whichever branch the compares select.
// A transaction that would exceed MaxStorageBytes is rejected with
// *quota.ExceededError before consensus; reserved bytes are released on error.
//
// Consistent with the KV quota path, puts are accounted by value length and
// deletes are not credited (the reconciler corrects the persisted counter for
// volume tenants).
type quotaTransactionService struct {
	inner    service.TransactionServicer
	acct     *quota.Accountant
	tenantID string
	limit    int64
}

// Compile-time interface check.
var (
	_ service.TransactionServicer  = (*quotaTransactionService)(nil)
	_ service.TransactionUnwrapper = (*quotaTransactionService)(nil)
)

// newQuotaTransactionService wraps inner with storage-byte quota enforcement for
// the given tenant and limit (MaxStorageBytes).
func newQuotaTransactionService(inner service.TransactionServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaTransactionService {
	return &quotaTransactionService{inner: inner, acct: acct, tenantID: tenantID, limit: limit}
}

// Unwrap returns the transaction service the quota decorates.
func (s *quotaTransactionService) Unwrap() service.TransactionServicer {
	return s.inner
}

// reserve admits n bytes against the tenant's quota.
func (s *quotaTransactionService) reserve(ctx context.Context, n int) error {
	return s.acct.Reserve(ctx, s.tenantID, s.limit, int64(n))
}

// branchPutBytes returns the larger put-byte total of the two mutually
// exclusive transaction branches.
func branchPutBytes(successOps, failureOps []service.Op) int {
	s := opPutBytes(successOps)
	if f := opPutBytes(failureOps); f > s {
		return f
	}
	return s
}

// entityBranchPutBytes returns the larger put-byte total of the two mutually
// exclusive entity-transaction branches.
func entityBranchPutBytes(success, failure []service.TransactionEntityOp) int {
	s := entityOpPutBytes(success)
	if f := entityOpPutBytes(failure); f > s {
		return f
	}
	return s
}

// entityOpPutBytes sums the data lengths of the put operations in ops.
func entityOpPutBytes(ops []service.TransactionEntityOp) int {
	var n int
	for _, op := range ops {
		if op.Type == "put" {
			n += len(op.Data)
		}
	}
	return n
}

// Execute admits the larger branch's put bytes against the quota, then forwards.
func (s *quotaTransactionService) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	n := branchPutBytes(successOps, failureOps)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.Execute(ctx, compares, successOps, failureOps)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// ExecuteWithMode admits the larger branch's put bytes against the quota, then
// forwards.
func (s *quotaTransactionService) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
	n := branchPutBytes(successOps, failureOps)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.ExecuteWithMode(ctx, compares, successOps, failureOps, mode)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// ExecuteEntity admits the larger entity branch's put bytes against the quota,
// then forwards.
func (s *quotaTransactionService) ExecuteEntity(ctx context.Context, req *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	var n int
	if req != nil {
		n = entityBranchPutBytes(req.Success, req.Failure)
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
