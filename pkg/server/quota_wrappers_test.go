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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// TestEntityBranchPutBytes_SelectsLarger verifies that entityBranchPutBytes
// returns the larger of the success/failure put-byte totals.
// countingTxService is a TransactionServicer double that counts the calls
// reaching it and returns err from each.
type countingTxService struct {
	service.TransactionServicer
	calls int
	err   error
}

func (c *countingTxService) Execute(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
	c.calls++
	return &service.TransactionResponse{}, c.err
}

func (c *countingTxService) ExecuteWithMode(context.Context, []service.Compare, []service.Op, []service.Op, writemode.Mode) (*service.TransactionResponse, error) {
	c.calls++
	return &service.TransactionResponse{}, c.err
}

func (c *countingTxService) ExecuteEntity(context.Context, *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	c.calls++
	return &service.TransactionEntityResponse{}, c.err
}

func TestEntityBranchPutBytes_SelectsLarger(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		success []service.TransactionEntityOp
		failure []service.TransactionEntityOp
		want    int
	}{
		{
			name:    "success larger",
			success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 100)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 50)}},
			want:    100,
		},
		{
			name:    "failure larger",
			success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 30)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 80)}},
			want:    80,
		},
		{
			name:    "equal",
			success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 40)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 40)}},
			want:    40,
		},
		{
			name:    "delete ops not counted",
			success: []service.TransactionEntityOp{{Type: "delete", Data: make([]byte, 200)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 10)}},
			want:    10,
		},
		{
			name:    "both empty",
			success: nil,
			failure: nil,
			want:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := entityBranchPutBytes(tc.success, tc.failure)
			if got != tc.want {
				t.Errorf("entityBranchPutBytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEntityOpPutBytes_SumsPuts verifies that entityOpPutBytes sums only
// the "put" operations and ignores deletes.
func TestEntityOpPutBytes_SumsPuts(t *testing.T) {
	t.Parallel()
	ops := []service.TransactionEntityOp{
		{Type: "put", Data: make([]byte, 20)},
		{Type: "delete", Data: make([]byte, 100)}, // must not be counted
		{Type: "put", Data: make([]byte, 30)},
	}
	got := entityOpPutBytes(ops)
	if got != 50 {
		t.Errorf("entityOpPutBytes = %d, want 50", got)
	}
}

// TestQuotaTransactionService_ExecuteWithMode_AccountsAndEnforces verifies that
// ExecuteWithMode reserves the larger branch's put bytes, forwards on success,
// and releases on inner error.
func TestQuotaTransactionService_ExecuteWithMode_AccountsAndEnforces(t *testing.T) {
	inner := &countingTxService{}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 100)
	ctx := context.Background()

	// success branch has 60 bytes — admitted.
	successOps := []service.Op{{Type: service.OpPut, Value: make([]byte, 60)}}
	_, err := tx.ExecuteWithMode(ctx, nil, successOps, nil, writemode.Mode(0))
	if err != nil {
		t.Fatalf("ExecuteWithMode within limit: %v", err)
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60", got)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}

	// A second batch of 50 bytes would exceed the 100-byte limit → rejected.
	bigOps := []service.Op{{Type: service.OpPut, Value: make([]byte, 50)}}
	_, err = tx.ExecuteWithMode(ctx, nil, bigOps, nil, writemode.Mode(0))
	if err == nil {
		t.Fatal("expected ExceededError, got nil")
	}
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Errorf("expected *quota.ExceededError, got %T: %v", err, err)
	}
	if inner.calls != 1 {
		t.Fatalf("rejected ExecuteWithMode must not reach inner (calls=%d)", inner.calls)
	}
}

// TestQuotaTransactionService_ExecuteWithMode_InnerErrorReleases verifies
// that reserved bytes are released when the inner service returns an error.
func TestQuotaTransactionService_ExecuteWithMode_InnerErrorReleases(t *testing.T) {
	inner := &countingTxService{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 1000)
	ctx := context.Background()

	ops := []service.Op{{Type: service.OpPut, Value: make([]byte, 50)}}
	if _, err := tx.ExecuteWithMode(ctx, nil, ops, nil, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteWithMode err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after inner error = %d, want 0 (released)", acct.Usage("t1"))
	}
}

// TestQuotaTransactionService_ExecuteEntity_AccountsAndEnforces verifies that
// ExecuteEntity reserves the larger entity-branch put bytes.
func TestQuotaTransactionService_ExecuteEntity_AccountsAndEnforces(t *testing.T) {
	inner := &countingTxService{}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 100)
	ctx := context.Background()

	req := &service.TransactionEntityRequest{
		Success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 40)}},
		Failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 60)}},
	}
	_, err := tx.ExecuteEntity(ctx, req)
	if err != nil {
		t.Fatalf("ExecuteEntity within limit: %v", err)
	}
	// failure branch is larger (60 bytes) → that's what was reserved.
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60 (larger branch)", got)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
}

// TestQuotaTransactionService_ExecuteEntity_NilReqZeroBytes verifies that
// a nil request reserves zero bytes and still forwards to the inner service.
func TestQuotaTransactionService_ExecuteEntity_NilReqZeroBytes(t *testing.T) {
	inner := &countingTxService{}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 10)
	ctx := context.Background()

	if _, err := tx.ExecuteEntity(ctx, nil); err != nil {
		t.Fatalf("ExecuteEntity(nil): %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage for nil req = %d, want 0", got)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
}

// TestQuotaTransactionService_ExecuteEntity_InnerErrorReleases verifies that
// reserved bytes are released on inner error.
func TestQuotaTransactionService_ExecuteEntity_InnerErrorReleases(t *testing.T) {
	inner := &countingTxService{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 1000)
	ctx := context.Background()

	req := &service.TransactionEntityRequest{
		Success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 30)}},
	}
	if _, err := tx.ExecuteEntity(ctx, req); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteEntity err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after inner error = %d, want 0 (released)", acct.Usage("t1"))
	}
}

// ---------------------------------------------------------------------------
// quota_batch.go — ExecuteWithMode, ExecuteEntity, ImportCommit
// ---------------------------------------------------------------------------

// cgCountingBatch is an inner BatchServicer that records reach-through and
// implements ImportCommit so the passthrough can be verified. (Named
// distinctly from the cgCountingBatch in quota_engines_test.go, which has a
// different shape.)
type cgCountingBatch struct {
	service.BatchServicer
	execCalls   int
	importCalls int
	err         error
}

func (m *cgCountingBatch) Execute(_ context.Context, ops []service.Op) (*service.BatchResponse, error) {
	m.execCalls++
	return &service.BatchResponse{Responses: make([]service.OpResponse, len(ops))}, m.err
}

func (m *cgCountingBatch) ExecuteWithMode(_ context.Context, ops []service.Op, _ writemode.Mode) (*service.BatchResponse, error) {
	m.execCalls++
	return &service.BatchResponse{Responses: make([]service.OpResponse, len(ops))}, m.err
}

func (m *cgCountingBatch) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	m.execCalls++
	return &service.BatchEntityResponse{}, m.err
}

func (m *cgCountingBatch) ImportBegin(_ context.Context, _ service.ImportOptions) (string, error) {
	return "", nil
}

func (m *cgCountingBatch) ImportAppend(_ context.Context, _ string, _ uint64, _ []byte) error {
	return nil
}

func (m *cgCountingBatch) ImportCommit(_ context.Context, _ string) (*service.ImportResult, error) {
	m.importCalls++
	return &service.ImportResult{}, m.err
}

func (m *cgCountingBatch) ImportAbort(_ context.Context, _ string) error {
	return nil
}

// TestQuotaBatchService_ExecuteWithMode_AccountsAndEnforces verifies that
// ExecuteWithMode reserves the batch's put bytes before forwarding.
func TestQuotaBatchService_ExecuteWithMode_AccountsAndEnforces(t *testing.T) {
	inner := &cgCountingBatch{}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 100)
	ctx := context.Background()

	ops := []service.Op{
		{Type: service.OpPut, Value: make([]byte, 70)},
	}
	if _, err := batch.ExecuteWithMode(ctx, ops, writemode.Mode(0)); err != nil {
		t.Fatalf("ExecuteWithMode within limit: %v", err)
	}
	if got := acct.Usage("t1"); got != 70 {
		t.Fatalf("usage = %d, want 70", got)
	}
	if inner.execCalls != 1 {
		t.Fatalf("inner execCalls = %d, want 1", inner.execCalls)
	}

	// A second batch that would exceed the limit is rejected.
	bigOps := []service.Op{{Type: service.OpPut, Value: make([]byte, 40)}}
	_, err := batch.ExecuteWithMode(ctx, bigOps, writemode.Mode(0))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit ExecuteWithMode err = %v, want *quota.ExceededError", err)
	}
	if inner.execCalls != 1 {
		t.Fatalf("rejected call must not reach inner (execCalls=%d)", inner.execCalls)
	}
}

// TestQuotaBatchService_ExecuteWithMode_InnerErrorReleases verifies the
// release-on-error path for ExecuteWithMode.
func TestQuotaBatchService_ExecuteWithMode_InnerErrorReleases(t *testing.T) {
	inner := &cgCountingBatch{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 1000)
	ctx := context.Background()

	ops := []service.Op{{Type: service.OpPut, Value: make([]byte, 50)}}
	if _, err := batch.ExecuteWithMode(ctx, ops, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteWithMode err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after error = %d, want 0 (released)", acct.Usage("t1"))
	}
}

// TestQuotaBatchService_ExecuteEntity_NilReq verifies nil request reserves
// zero bytes and forwards to the inner service.
func TestQuotaBatchService_ExecuteEntity_NilReq(t *testing.T) {
	inner := &cgCountingBatch{}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 10)
	ctx := context.Background()

	if _, err := batch.ExecuteEntity(ctx, nil); err != nil {
		t.Fatalf("ExecuteEntity(nil): %v", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage for nil req = %d, want 0", acct.Usage("t1"))
	}
	if inner.execCalls != 1 {
		t.Fatalf("inner execCalls = %d, want 1", inner.execCalls)
	}
}

// TestQuotaBatchService_ExecuteEntity_InnerErrorReleases verifies the
// release-on-error path for ExecuteEntity: bytes reserved for an entity batch
// the inner service then refused are returned to the tenant.
func TestQuotaBatchService_ExecuteEntity_InnerErrorReleases(t *testing.T) {
	inner := &cgCountingBatch{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 1000)

	req := &service.BatchEntityRequest{Ops: []service.BatchEntityOp{{Type: "put", Data: make([]byte, 60)}}}
	if _, err := batch.ExecuteEntity(context.Background(), req); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteEntity err = %v, want errBoom", err)
	}
	if inner.execCalls != 1 {
		t.Fatalf("inner execCalls = %d, want 1", inner.execCalls)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after inner error = %d, want 0 (released)", got)
	}
}

// importRecordingBatch records every chunked-import call it receives and
// answers with fixed values, so a wrapper's forwarding can be checked
// argument by argument.
type importRecordingBatch struct {
	service.BatchServicer
	calls      []string
	beginOpts  service.ImportOptions
	appendSeq  uint64
	appendData []byte
}

func (m *importRecordingBatch) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	m.calls = append(m.calls, "begin")
	m.beginOpts = opts
	return "sess-1", nil
}

func (m *importRecordingBatch) ImportAppend(_ context.Context, sessionID string, seq uint64, chunk []byte) error {
	m.calls = append(m.calls, "append:"+sessionID)
	m.appendSeq, m.appendData = seq, chunk
	return nil
}

func (m *importRecordingBatch) ImportCommit(_ context.Context, sessionID string) (*service.ImportResult, error) {
	m.calls = append(m.calls, "commit:"+sessionID)
	return &service.ImportResult{}, nil
}

func (m *importRecordingBatch) ImportAbort(_ context.Context, sessionID string) error {
	m.calls = append(m.calls, "abort:"+sessionID)
	return errBoom
}

// TestQuotaBatchService_ChunkedImportForwardsUnmetered pins the documented
// division of quotas: a chunked import session is bounded by its own session
// limits inside BatchService, not by this wrapper's tenant storage-byte
// quota. Every call reaches the inner service verbatim -- options, session
// ID, sequence and chunk, and the inner error -- and a chunk far larger than
// the tenant's remaining quota is neither refused nor charged here.
func TestQuotaBatchService_ChunkedImportForwardsUnmetered(t *testing.T) {
	inner := &importRecordingBatch{}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 10)
	ctx := context.Background()

	opts := service.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row", KeyPrefix: "p/"}
	sessionID, err := batch.ImportBegin(ctx, opts)
	if err != nil || sessionID != "sess-1" {
		t.Fatalf("ImportBegin = (%q, %v), want (sess-1, nil)", sessionID, err)
	}
	if inner.beginOpts != opts {
		t.Fatalf("inner ImportBegin opts = %+v, want %+v", inner.beginOpts, opts)
	}

	chunk := make([]byte, 100) // ten times the tenant's whole quota
	if err := batch.ImportAppend(ctx, sessionID, 7, chunk); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}
	if inner.appendSeq != 7 || len(inner.appendData) != len(chunk) {
		t.Fatalf("inner ImportAppend got seq=%d len=%d, want seq=7 len=%d", inner.appendSeq, len(inner.appendData), len(chunk))
	}
	if _, err := batch.ImportCommit(ctx, sessionID); err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if err := batch.ImportAbort(ctx, sessionID); !errors.Is(err, errBoom) {
		t.Fatalf("ImportAbort err = %v, want the inner service's error", err)
	}

	want := []string{"begin", "append:sess-1", "commit:sess-1", "abort:sess-1"}
	if !slices.Equal(inner.calls, want) {
		t.Fatalf("inner calls = %v, want %v", inner.calls, want)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("tenant storage usage = %d after an import session, want 0 (not metered by this wrapper)", got)
	}
}

// TestQuotaBatchService_Import_PropagatesError verifies that inner errors
// are propagated correctly from ImportCommit.
func TestQuotaBatchService_Import_PropagatesError(t *testing.T) {
	inner := &cgCountingBatch{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 100)
	ctx := context.Background()

	if _, err := batch.ImportCommit(ctx, "sess"); !errors.Is(err, errBoom) {
		t.Fatalf("ImportCommit error = %v, want errBoom", err)
	}
}

// ---------------------------------------------------------------------------
// Server option setters — WithClusterKeyReceiver, WithBackupService,
//                         WithRebalanceService, WithTenantHostRegistry,
//                         WithRaftBackendFactory, WithQuotaAccountant
// ---------------------------------------------------------------------------
