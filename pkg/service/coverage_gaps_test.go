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

// coverage_gaps_test.go covers functions that were at 0% or low coverage
// in the pkg/service package under -tags "codec_json raft".
package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Helpers shared across tests in this file
// ---------------------------------------------------------------------------

func newCoverageLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newCoverageDatabaseManager(t *testing.T) *database.Manager {
	t.Helper()
	dir := t.TempDir()
	mgr := database.NewManager(newCoverageLogger(), dir)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// ---------------------------------------------------------------------------
// service.go – KVService stale / write-mode / consistency operations (0%)
// ---------------------------------------------------------------------------

func TestKVService_ListStale_Error(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1}
		},
	}
	svc := NewKVService(host, 1)
	keys, err := svc.ListStale(context.Background(), "pfx")
	if keys != nil {
		t.Fatal("expected nil keys on error")
	}
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestKVService_ScanStale_Error(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("stale read failure")
		},
	}
	svc := NewKVService(host, 1)
	result, err := svc.ScanStale(context.Background(), "pfx")
	if result != nil {
		t.Fatal("expected nil result on error")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_PutWithMode_NotLeaderError(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 2}
		},
	}
	svc := NewKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "k", []byte("v"), writemode.Sync)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestKVService_PutWithMode_GenericError(t *testing.T) {
	sentinel := errors.New("write stall")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	svc := NewKVService(host, 1)
	err := svc.PutWithMode(context.Background(), "k", []byte("v"), writemode.Sync)
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
}

func TestKVService_DeleteWithMode_GenericError(t *testing.T) {
	sentinel := errors.New("disk io error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	svc := NewKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "k", writemode.WALDurable)
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected sentinel cause, got %v", pe.Unwrap())
	}
}

func TestKVService_DeleteWithMode_NotLeader(t *testing.T) {
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 5}
		},
	}
	svc := NewKVService(host, 1)
	err := svc.DeleteWithMode(context.Background(), "k", writemode.Sync)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.LeaderID != 5 {
		t.Fatalf("expected LeaderID 5, got %d", nle.LeaderID)
	}
}

func TestKVService_GetWithConsistency_Nil(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}
	svc := NewKVService(host, 1)
	val, err := svc.GetWithConsistency(context.Background(), "missing", consistency.ReadOptions{})
	if val != nil {
		t.Fatalf("expected nil val, got %v", val)
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_GetWithConsistency_Error(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 2}
		},
	}
	svc := NewKVService(host, 2)
	_, err := svc.GetWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

func TestKVService_ListWithConsistency_Error(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("timeout")
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.ListWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ListWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return map[string]string{}, nil
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.ListWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ScanWithConsistency_Error(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("network error")
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.ScanWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ScanWithConsistency_UnexpectedType(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return "notamap", nil
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.ScanWithConsistency(context.Background(), "", consistency.ReadOptions{})
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

func TestKVService_ExistsWithConsistency_Exists(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return []byte("v"), nil
		},
	}
	svc := NewKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
}

func TestKVService_ExistsWithConsistency_NilResultIsFalse(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, nil
		},
	}
	svc := NewKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for nil result")
	}
}

func TestKVService_ExistsWithConsistency_KeyNotFoundReturnsFalse(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, &KeyNotFoundError{Key: "k"}
		},
	}
	svc := NewKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for KeyNotFoundError")
	}
}

func TestKVService_ExistsWithConsistency_OtherError(t *testing.T) {
	host := &mockHost{
		readWithConsistencyFn: func(_ context.Context, _ uint64, _ interface{}, _ consistency.ReadOptions) (interface{}, error) {
			return nil, errors.New("network timeout")
		},
	}
	svc := NewKVService(host, 1)
	exists, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if exists {
		t.Fatal("expected exists=false on error")
	}
}

// filterSystemMap – verify system keys are stripped from non-system prefixes
// but retained when targeting the system namespace.
func TestFilterSystemMap_StripsSysKeys(t *testing.T) {
	data := map[string][]byte{
		"user:alice":    {1},
		"__sys:config":  {2},
		"__sys:version": {3},
	}
	result := filterSystemMap(data, "user:")
	if _, ok := result["user:alice"]; !ok {
		t.Fatal("expected user:alice to be retained")
	}
	if _, ok := result["__sys:config"]; ok {
		t.Fatal("expected __sys:config to be removed")
	}
}

func TestFilterSystemMap_RetainsSysKeysWhenSysPrefix(t *testing.T) {
	data := map[string][]byte{
		"__sys:config":  {1},
		"__sys:version": {2},
	}
	result := filterSystemMap(data, "__sys:")
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
}

func TestFilterSystemMap_EmptyMap(t *testing.T) {
	result := filterSystemMap(map[string][]byte{}, "pfx")
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %d entries", len(result))
	}
}

// ---------------------------------------------------------------------------
// batch_service.go – ExecuteWithMode, executeGet, executeRange,
//                    translateReadError (all 0%)
// ---------------------------------------------------------------------------

func TestBatchService_ExecuteWithMode_PutAndDelete(t *testing.T) {
	var capturedMode writemode.Mode
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, mode writemode.Mode) (sm.Result, error) {
			capturedMode = mode
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: OpDelete, Key: []byte("k2")},
	}
	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.WALDurable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
	for i, r := range responses {
		if !r.Succeeded {
			t.Fatalf("response[%d].Succeeded=false, want true", i)
		}
	}
	if capturedMode != writemode.WALDurable {
		t.Fatalf("expected WALDurable mode, got %v", capturedMode)
	}
}

func TestBatchService_ExecuteWithMode_ReadGet(t *testing.T) {
	// executeGet is called when batch includes an OpGet.
	// Mock returns a *KeyValue so the type assertion succeeds.
	kvEntry := &KeyValue{Key: []byte("k"), Value: []byte("v")}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return kvEntry, nil
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpGet, Key: []byte("k")},
	}
	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.Sync)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Succeeded {
		t.Fatal("expected Succeeded=true")
	}
	if len(responses[0].Kvs) != 1 {
		t.Fatalf("expected 1 kv, got %d", len(responses[0].Kvs))
	}
}

func TestBatchService_ExecuteWithMode_ReadRange(t *testing.T) {
	// executeRange is called when batch includes an OpRange.
	rr := &MVCCRangeResult{
		KeyValues: []*KeyValue{
			{Key: []byte("a"), Value: []byte("1")},
			{Key: []byte("b"), Value: []byte("2")},
		},
	}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return rr, nil
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
	}
	responses, err := svc.ExecuteWithMode(context.Background(), ops, writemode.Sync)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].Count != 2 {
		t.Fatalf("expected Count=2, got %d", responses[0].Count)
	}
}

// executeGet path: key-not-found returns succeeded with empty Kvs.
// We use mockKeyNotFoundErr which satisfies the keyNotFoundErr interface
// (has both Error() and KeyNotFound() methods).
func TestBatchService_Execute_GetKeyNotFound(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &mockKeyNotFoundErr{key: "missing"}
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{
		{Type: OpGet, Key: []byte("missing")},
	}
	responses, err := svc.Execute(context.Background(), ops)
	if err != nil {
		t.Fatalf("unexpected error on key-not-found: %v", err)
	}
	if !responses[0].Succeeded {
		t.Fatal("expected Succeeded=true for missing key")
	}
	if len(responses[0].Kvs) != 0 {
		t.Fatalf("expected empty Kvs, got %d", len(responses[0].Kvs))
	}
}

// executeGet path: read error (non-knf) propagates.
func TestBatchService_Execute_GetReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1}
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{{Type: OpGet, Key: []byte("k")}}
	_, err := svc.Execute(context.Background(), ops)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
}

// executeGet with RangeEnd set dispatches to executeRange.
func TestBatchService_Execute_GetWithRangeEnd(t *testing.T) {
	rr := &MVCCRangeResult{
		KeyValues: []*KeyValue{{Key: []byte("a"), Value: []byte("1")}},
	}
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
			return rr, nil
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{{Type: OpGet, Key: []byte("a"), RangeEnd: []byte("z")}}
	responses, err := svc.Execute(context.Background(), ops)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if responses[0].Count != 1 {
		t.Fatalf("expected Count=1, got %d", responses[0].Count)
	}
}

// executeGet: unexpected result type returns ReadError.
func TestBatchService_Execute_GetUnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "wrong", nil
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{{Type: OpGet, Key: []byte("k")}}
	_, err := svc.Execute(context.Background(), ops)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// executeRange: read error propagates.
func TestBatchService_Execute_RangeReadError(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("range fail")
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{{Type: OpRange, Key: []byte("a"), RangeEnd: []byte("z")}}
	_, err := svc.Execute(context.Background(), ops)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// executeRange: unexpected result type returns ReadError.
func TestBatchService_Execute_RangeUnexpectedType(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return "notarangeresult", nil
		},
	}
	svc := NewBatchService(host, 1)
	ops := []Op{{Type: OpRange, Key: []byte("a"), RangeEnd: []byte("z")}}
	_, err := svc.Execute(context.Background(), ops)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T: %v", err, err)
	}
}

// translateReadError: NotLeaderError maps to service-layer NotLeaderError.
func TestBatchService_TranslateReadError_NotLeader(t *testing.T) {
	host := &mockHost{
		syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 7, LeaderID: 3, LeaderAddress: "h:9"}
		},
	}
	svc := NewBatchService(host, 7)
	ops := []Op{{Type: OpRange, Key: []byte("a"), RangeEnd: []byte("z")}}
	_, err := svc.Execute(context.Background(), ops)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T: %v", err, err)
	}
	if nle.ShardID != 7 {
		t.Fatalf("expected ShardID 7, got %d", nle.ShardID)
	}
}

// ---------------------------------------------------------------------------
// transaction_service.go – ExecuteWithMode (0%)
// ---------------------------------------------------------------------------

func TestTransactionService_ExecuteWithMode_GenericProposeError(t *testing.T) {
	sentinel := errors.New("mode error")
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			return sm.Result{}, sentinel
		},
	}
	svc := NewTransactionService(host, 1)
	_, err := svc.ExecuteWithMode(context.Background(), nil, nil, nil, writemode.Sync)
	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProposeError, got %T: %v", err, err)
	}
	if !errors.Is(pe.Unwrap(), sentinel) {
		t.Fatalf("expected sentinel cause, got %v", pe.Unwrap())
	}
}

func TestTransactionService_ExecuteWithMode_SuccessOps(t *testing.T) {
	// Verify that successOps are reflected in the response.
	host := &mockHost{
		proposeWithModeFn: func(_ context.Context, _ uint64, _ []byte, _ writemode.Mode) (sm.Result, error) {
			// Data[0] = 1 means compares passed → success ops applied.
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}
	svc := NewTransactionService(host, 1)
	successOps := []Op{
		{Type: OpPut, Key: []byte("sk"), Value: []byte("sv")},
	}
	resp, err := svc.ExecuteWithMode(context.Background(), nil, successOps, nil, writemode.Sync)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected Succeeded=true")
	}
}

// ---------------------------------------------------------------------------
// credential_service.go – CredentialList, CredentialRotate,
//                          CredentialRetrieveVersion, CredentialListVersions,
//                          CredentialGCExpired (all 0%)
// ---------------------------------------------------------------------------

func newVersionedCredentialService(t *testing.T) CredentialServicer {
	t.Helper()
	cfg := &credentials.Config{Strategy: credentials.StrategyManual}
	inner, err := credentials.NewCredentialService(cfg, nil, testLogger())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	return NewCredentialService(inner)
}

func TestCredentialService_CredentialList_Empty(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	names, err := svc.CredentialList(ctx)
	if err != nil {
		t.Fatalf("CredentialList returned unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 credentials, got %d", len(names))
	}
}

func TestCredentialService_CredentialList_AfterStore(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	if err := svc.CredentialStore(ctx, "cred-a", []byte("val-a")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	if err := svc.CredentialStore(ctx, "cred-b", []byte("val-b")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}

	names, err := svc.CredentialList(ctx)
	if err != nil {
		t.Fatalf("CredentialList returned unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(names))
	}
}

func TestCredentialService_CredentialRotate_Success(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	if err := svc.CredentialStore(ctx, "rot-cred", []byte("initial")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}

	opts := credentials.RotateOptions{
		NewValue:    []byte("rotated"),
		GracePeriod: 0,
	}
	result, err := svc.CredentialRotate(ctx, "rot-cred", opts)
	if err != nil {
		t.Fatalf("CredentialRotate returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil RotateResult")
	}
	if result.NewVersion == 0 {
		t.Fatal("expected non-zero NewVersion after rotate")
	}
}

func TestCredentialService_CredentialRotate_NewCredential(t *testing.T) {
	// rotateManual creates version 1 outright when the credential does not exist.
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	opts := credentials.RotateOptions{NewValue: []byte("first")}
	result, err := svc.CredentialRotate(ctx, "brand-new", opts)
	if err != nil {
		t.Fatalf("CredentialRotate returned unexpected error: %v", err)
	}
	if result.NewVersion != 1 {
		t.Fatalf("expected NewVersion=1 for new credential, got %d", result.NewVersion)
	}
}

func TestCredentialService_CredentialRetrieveVersion_ExplicitVersion(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	if err := svc.CredentialStore(ctx, "ver-cred", []byte("v1")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}

	// Version 1 retrieves the credential stored above (legacy untracked path).
	val, err := svc.CredentialRetrieveVersion(ctx, "ver-cred", 1)
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion returned unexpected error: %v", err)
	}
	if string(val) != "v1" {
		t.Fatalf("expected v1, got %s", string(val))
	}
}

func TestCredentialService_CredentialRetrieveVersion_VersionNotFound(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	// Request version 99 for an existing credential — should return an error
	// because version 99 was never written.
	if err := svc.CredentialStore(ctx, "ghost", []byte("v1")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}

	_, err := svc.CredentialRetrieveVersion(ctx, "ghost", 99)
	if err == nil {
		t.Fatal("expected error for non-existent version, got nil")
	}
}

func TestCredentialService_CredentialListVersions_Single(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	if err := svc.CredentialStore(ctx, "history-cred", []byte("val1")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}

	versions, err := svc.CredentialListVersions(ctx, "history-cred")
	if err != nil {
		t.Fatalf("CredentialListVersions returned unexpected error: %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("expected at least one version")
	}
}

func TestCredentialService_CredentialListVersions_NotFound(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	_, err := svc.CredentialListVersions(ctx, "phantom")
	if err == nil {
		t.Fatal("expected error for non-existent credential")
	}
}

func TestCredentialService_CredentialGCExpired_NothingToGC(t *testing.T) {
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	if err := svc.CredentialStore(ctx, "gc-cred", []byte("val")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}

	// Nothing expired → count must be 0.
	n, err := svc.CredentialGCExpired(ctx, "gc-cred")
	if err != nil {
		t.Fatalf("CredentialGCExpired returned unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 reaped, got %d", n)
	}
}

func TestCredentialService_CredentialGCExpired_MissingCredReturnsZero(t *testing.T) {
	// gcManual returns 0 (not an error) when no metadata exists for the
	// credential — it is safe to call GCExpired on any name.
	svc := newVersionedCredentialService(t)
	ctx := context.Background()

	n, err := svc.CredentialGCExpired(ctx, "ghost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 reaped for missing cred, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// database_service.go – WithBarrierManager, UpdateEtcd, UnsealDatabase,
//                        GetForTenant, ListForTenant, DropForTenant,
//                        UpdateProtocolForTenant, UpdateEtcdForTenant,
//                        UpdateRaftForTenant, UpdateBarrierForTenant,
//                        UpdateConnectionForTenant (all 0%)
// ---------------------------------------------------------------------------

func TestDatabaseService_WithBarrierManager_Option(t *testing.T) {
	// Without the WithBarrierManager option, UnsealDatabase should return
	// BarrierManagerNotConfiguredError because barrierMgr is nil.
	svc := newTestDatabaseService(t)
	err := svc.UnsealDatabase(context.Background(), "any", seal.Credentials{})
	var bmErr *BarrierManagerNotConfiguredError
	if !errors.As(err, &bmErr) {
		t.Fatalf("expected *BarrierManagerNotConfiguredError without option, got %T: %v", err, err)
	}
}

func newTenantDatabaseService(t *testing.T) DatabaseServicer {
	t.Helper()
	return NewDatabaseService(newCoverageDatabaseManager(t))
}

func TestDatabaseService_GetForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.GetForTenant(ctx, "no-such-db")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseService_ListForTenant_Empty(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	infos, err := svc.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant returned unexpected error: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected 0 databases, got %d", len(infos))
	}
}

func TestDatabaseService_ListForTenant_AfterCreate(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-db1")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if _, err := svc.CreateForTenant(ctx, testDBConfig("t-db2")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	infos, err := svc.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant returned unexpected error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(infos))
	}
}

func TestDatabaseService_DropForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	err := svc.DropForTenant(ctx, "ghost-db")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseService_UpdateProtocolForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.UpdateProtocolForTenant(ctx, "no-db", database.ProtocolConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseService_UpdateEtcdForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.UpdateEtcdForTenant(ctx, "no-db", &database.EtcdConfig{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseService_UpdateRaftForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.UpdateRaftForTenant(ctx, "no-db", &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseService_UpdateBarrierForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.UpdateBarrierForTenant(ctx, "no-db", &database.DatabaseBarrierConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseService_UpdateConnectionForTenant_NotFound(t *testing.T) {
	svc := newTenantDatabaseService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := svc.UpdateConnectionForTenant(ctx, "no-db", &database.DatabaseConnectionConfig{Transport: "grpc", Address: "h:9"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// collection_service.go – WithDatabaseCodecLookup, resolveCodec,
//                          marshalDocument, unmarshalDocument,
//                          extractExplicitID (0% or very low)
// ---------------------------------------------------------------------------

func TestCollectionService_WithDatabaseCodecLookup_UsedWhenCollectionHasNoCodec(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	// Register a db-level codec fallback.
	svc = svc.WithDatabaseCodecLookup(func(db string) string {
		if db == "mydb" {
			return "json" // always available in codec_json build
		}
		return ""
	})

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "col-with-db-codec",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection returned unexpected error: %v", err)
	}

	// Insert should succeed, meaning the codec resolved correctly.
	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "col-with-db-codec",
		Database:   "mydb",
		Document:   map[string]interface{}{"name": "test"},
	})
	if err != nil {
		t.Fatalf("InsertDocument returned unexpected error: %v", err)
	}
}

func TestCollectionService_WithDatabaseCodecLookup_FallbackWhenEmpty(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	// Codec lookup returns empty string → should fall back to JSON.
	svc = svc.WithDatabaseCodecLookup(func(_ string) string { return "" })

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "fallback-col",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection returned unexpected error: %v", err)
	}
}

// extractExplicitID: various _id types.
func TestExtractExplicitID_Uint64(t *testing.T) {
	doc := map[string]interface{}{"_id": uint64(42)}
	if got := extractExplicitID(doc); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}

func TestExtractExplicitID_Int64Positive(t *testing.T) {
	doc := map[string]interface{}{"_id": int64(99)}
	if got := extractExplicitID(doc); got != 99 {
		t.Fatalf("expected 99, got %d", got)
	}
}

func TestExtractExplicitID_Int64Negative(t *testing.T) {
	doc := map[string]interface{}{"_id": int64(-1)}
	if got := extractExplicitID(doc); got != 0 {
		t.Fatalf("expected 0 for negative int64, got %d", got)
	}
}

func TestExtractExplicitID_Float64Positive(t *testing.T) {
	doc := map[string]interface{}{"_id": float64(7)}
	if got := extractExplicitID(doc); got != 7 {
		t.Fatalf("expected 7, got %d", got)
	}
}

func TestExtractExplicitID_Float64NonPositive(t *testing.T) {
	doc := map[string]interface{}{"_id": float64(0)}
	if got := extractExplicitID(doc); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestExtractExplicitID_IntPositive(t *testing.T) {
	doc := map[string]interface{}{"_id": int(55)}
	if got := extractExplicitID(doc); got != 55 {
		t.Fatalf("expected 55, got %d", got)
	}
}

func TestExtractExplicitID_IntNonPositive(t *testing.T) {
	doc := map[string]interface{}{"_id": int(0)}
	if got := extractExplicitID(doc); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestExtractExplicitID_Missing(t *testing.T) {
	doc := map[string]interface{}{"name": "Alice"}
	if got := extractExplicitID(doc); got != 0 {
		t.Fatalf("expected 0 for missing _id, got %d", got)
	}
}

func TestExtractExplicitID_UnknownType(t *testing.T) {
	doc := map[string]interface{}{"_id": "string-id"}
	if got := extractExplicitID(doc); got != 0 {
		t.Fatalf("expected 0 for string _id, got %d", got)
	}
}

// resolveCodec: tested indirectly via WithDatabaseCodecLookup paths above,
// plus the invalid-codec branch via CreateCollection.
func TestCollectionService_CreateCollection_InvalidCodec(t *testing.T) {
	svc := newTestCollectionService()
	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name:         "bad-codec-col",
		Database:     "mydb",
		StorageCodec: "nonexistent-codec-xyz",
	})
	if err == nil {
		t.Fatal("expected error for invalid codec, got nil")
	}
	var ice *CollectionInvalidCodecError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *CollectionInvalidCodecError, got %T: %v", err, err)
	}
}

// CollectionInvalidCodecError.Error() – was 0%.
func TestCollectionInvalidCodecError_Error(t *testing.T) {
	e := &CollectionInvalidCodecError{Codec: "msgpack"}
	want := `service: invalid or unsupported storage codec: "msgpack"`
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// embedding_errors.go – all Error() / Unwrap() methods (0%)
// ---------------------------------------------------------------------------

func TestEmbeddingProviderRegistrationError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("dup")
	e := &EmbeddingProviderRegistrationError{Provider: "ollama", Cause: cause}
	want := "service: embedding provider registration failed: ollama: dup"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is should match cause through Unwrap")
	}
}

func TestEmbeddingProviderNotFoundError_Error(t *testing.T) {
	e := &EmbeddingProviderNotFoundError{Provider: "openai"}
	want := "service: embedding provider not found: openai"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestEmbeddingBatchTooLargeError_Error(t *testing.T) {
	e := &EmbeddingBatchTooLargeError{Requested: 200, Maximum: 100}
	want := "service: embedding batch too large: requested 200, maximum 100"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestEmbeddingDimensionMismatchError_Error(t *testing.T) {
	e := &EmbeddingDimensionMismatchError{Expected: 768, Received: 512}
	want := "service: embedding dimension mismatch: expected 768, received 512"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestEmbeddingServiceClosedError_Error(t *testing.T) {
	e := &EmbeddingServiceClosedError{}
	want := "service: embedding service is closed"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// embedding_service.go – EmbeddingProviderAlreadyRegisteredError,
//
//	EmbeddingCloseError (0%)
func TestEmbeddingProviderAlreadyRegisteredError_Error(t *testing.T) {
	e := &EmbeddingProviderAlreadyRegisteredError{Name: "openai"}
	want := "service: embedding provider already registered: openai"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestEmbeddingCloseError_ErrorAndUnwrap(t *testing.T) {
	e1 := errors.New("close err 1")
	e2 := errors.New("close err 2")
	e := &EmbeddingCloseError{Errors: []error{e1, e2}}
	got := e.Error()
	if got == "" {
		t.Fatal("expected non-empty error message")
	}
	unwrapped := e.Unwrap()
	if len(unwrapped) != 2 {
		t.Fatalf("expected 2 unwrapped errors, got %d", len(unwrapped))
	}
}

// ---------------------------------------------------------------------------
// rebalance_service.go – shardNotLoadedError.Error, nilPlanError.Error (0%)
// ---------------------------------------------------------------------------

func TestShardNotLoadedError_Error(t *testing.T) {
	e := &shardNotLoadedError{ShardID: 42}
	got := e.Error()
	if got != "shard not loaded: 42" {
		t.Fatalf("got %q, want %q", got, "shard not loaded: 42")
	}
}

func TestNilPlanError_Error(t *testing.T) {
	e := &nilPlanError{}
	want := "rebalance plan must not be nil"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// formatUint64 is already covered by rebalance_service_test.go;
// shardNotLoadedError and nilPlanError cover the 0% lines we care about.

// ---------------------------------------------------------------------------
// NewKVService with options – covers the options loop (75% → 100%)
// ---------------------------------------------------------------------------

func TestNewKVService_WithOptions(t *testing.T) {
	var optApplied bool
	opt := KVServiceOption(func(s *KVService) { optApplied = true })
	svc := NewKVService(&mockHost{}, 1, opt)
	if svc == nil {
		t.Fatal("expected non-nil KVService")
	}
	if !optApplied {
		t.Fatal("expected option to be applied")
	}
}

// ---------------------------------------------------------------------------
// GetStale – additional branches (66.7% → higher)
// ---------------------------------------------------------------------------

func TestKVService_GetStale_Success(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return []byte("stale-val"), nil
		},
	}
	svc := NewKVService(host, 1)
	val, err := svc.GetStale(context.Background(), "k")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(val) != "stale-val" {
		t.Fatalf("expected stale-val, got %s", string(val))
	}
}

func TestKVService_GetStale_UnexpectedType(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return 999, nil
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.GetStale(context.Background(), "k")
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ReadError, got %T", err)
	}
}

func TestKVService_GetStale_Error(t *testing.T) {
	host := &mockHost{
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, &quicraft.NotLeaderError{ShardID: 1}
		},
	}
	svc := NewKVService(host, 1)
	_, err := svc.GetStale(context.Background(), "k")
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("expected *NotLeaderError, got %T", err)
	}
}
