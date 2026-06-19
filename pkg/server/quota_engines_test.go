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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// M-1 — storage quota enforcement on the Batch and Transaction engines, which
// commit KV/MVCC Puts directly to the host, bypassing the wrapped KV/MVCC
// servicers. Before the fix applyQuota wrapped only KV+MVCC, so batched and
// transactional writes were not metered against MaxStorageBytes.
// ---------------------------------------------------------------------------

// countingBatch is an inner BatchServicer that records whether it was reached,
// so a quota rejection (which must short-circuit before the inner call) is
// observable.
type countingBatch struct {
	service.BatchServicer
	executed int
}

func (m *countingBatch) Execute(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
	m.executed++
	return nil, nil
}

func (m *countingBatch) ExecuteWithMode(_ context.Context, _ []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	m.executed++
	return nil, nil
}

func (m *countingBatch) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	m.executed++
	return &service.BatchEntityResponse{}, nil
}

func putOp(n int) service.Op {
	return service.Op{Type: service.OpPut, Key: []byte("k"), Value: make([]byte, n)}
}

func TestQuotaBatchService_EnforcesAndAccounts(t *testing.T) {
	inner := &countingBatch{}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaBatchService(inner, acct, "t1", 100)
	ctx := context.Background()

	// A batch within the limit is admitted and accounted.
	if _, err := q.Execute(ctx, []service.Op{putOp(60)}); err != nil {
		t.Fatalf("Execute within limit: %v", err)
	}
	if acct.Usage("t1") != 60 {
		t.Fatalf("usage = %d, want 60", acct.Usage("t1"))
	}
	if inner.executed != 1 {
		t.Fatalf("inner Execute calls = %d, want 1", inner.executed)
	}

	// A batch that would exceed MaxStorageBytes is rejected before consensus.
	_, err := q.Execute(ctx, []service.Op{putOp(50)})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit Execute err = %v, want *quota.ExceededError", err)
	}
	if inner.executed != 1 {
		t.Fatalf("rejected batch must not reach inner store (executed=%d)", inner.executed)
	}
	if acct.Usage("t1") != 60 {
		t.Fatalf("usage after rejection = %d, want 60", acct.Usage("t1"))
	}
}

func TestQuotaBatchService_ExecuteWithModeAndEntityEnforce(t *testing.T) {
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaBatchService(&countingBatch{}, acct, "t1", 100)
	ctx := context.Background()

	_, err := q.ExecuteWithMode(ctx, []service.Op{putOp(200)}, writemode.Mode(0))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("ExecuteWithMode over-limit err = %v, want *quota.ExceededError", err)
	}

	_, err = q.ExecuteEntity(ctx, &service.BatchEntityRequest{
		Ops: []service.BatchEntityOp{{Type: "put", ID: "e", Data: make([]byte, 200)}},
	})
	if !errors.As(err, &ee) {
		t.Fatalf("ExecuteEntity over-limit err = %v, want *quota.ExceededError", err)
	}
}

func TestQuotaBatchService_InnerErrorReleases(t *testing.T) {
	inner := &failingBatch{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaBatchService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if _, err := q.Execute(ctx, []service.Op{putOp(100)}); !errors.Is(err, errBoom) {
		t.Fatalf("Execute err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after failed batch = %d, want 0 (released)", acct.Usage("t1"))
	}
}

type failingBatch struct {
	service.BatchServicer
	err error
}

func (m *failingBatch) Execute(_ context.Context, _ []service.Op) ([]service.OpResponse, error) {
	return nil, m.err
}

// countingTxn is an inner TransactionServicer that records reach-through.
type countingTxn struct {
	service.TransactionServicer
	executed int
}

func (m *countingTxn) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	m.executed++
	return &service.TransactionResponse{}, nil
}

func TestQuotaTransactionService_EnforcesLargerBranch(t *testing.T) {
	inner := &countingTxn{}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaTransactionService(inner, acct, "t1", 100)
	ctx := context.Background()

	// The failure branch (120 bytes) is larger than the success branch (10) and
	// exceeds the limit, so the transaction is rejected even though the success
	// branch would fit — the admitted amount must cover whichever branch runs.
	_, err := q.Execute(ctx,
		[]service.Compare{{Key: []byte("k")}},
		[]service.Op{putOp(10)},
		[]service.Op{putOp(120)})
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit transaction err = %v, want *quota.ExceededError", err)
	}
	if inner.executed != 0 {
		t.Fatalf("rejected transaction must not reach inner store (executed=%d)", inner.executed)
	}

	// A transaction whose larger branch fits is admitted.
	if _, err := q.Execute(ctx, nil, []service.Op{putOp(40)}, []service.Op{putOp(80)}); err != nil {
		t.Fatalf("within-limit transaction: %v", err)
	}
	if acct.Usage("t1") != 80 {
		t.Fatalf("usage = %d, want 80 (larger branch)", acct.Usage("t1"))
	}
}

func TestQuotaTransactionService_InnerErrorReleases(t *testing.T) {
	inner := &failingTxn{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaTransactionService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if _, err := q.Execute(ctx, nil, []service.Op{putOp(100)}, nil); !errors.Is(err, errBoom) {
		t.Fatalf("Execute err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after failed transaction = %d, want 0 (released)", acct.Usage("t1"))
	}
}

type failingTxn struct {
	service.TransactionServicer
	err error
}

func (m *failingTxn) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	return nil, m.err
}
