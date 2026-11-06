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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockMVCCSvc embeds mockKVService and adds MVCC-specific methods.
// ---------------------------------------------------------------------------

type mockMVCCSvc struct {
	*mockKVService

	mu sync.Mutex

	putLeaseErr   error
	getRevErr     error
	getAtRevErr   error
	rangeErr      error
	compactErr    error
	currentRevErr error

	lastLeaseID    int64
	lastRevision   int64
	lastRangeStart []byte
	lastRangeEnd   []byte
	lastCompact    int64

	defaultKV     *kv.KeyValue
	rangeKVs      []*kv.KeyValue
	rangeCount    int64
	rangeRevision int64
	currentRev    int64

	restoreErr    error
	restoreResult *service.RestoreRevisionResult
	lastRestore   *service.RestoreRevisionRequest
}

var _ service.MVCCKVServicer = (*mockMVCCSvc)(nil)

func newMockMVCCSvc() *mockMVCCSvc {
	dkv := &kv.KeyValue{
		Key:            []byte("k1"),
		Value:          []byte("v1"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        1,
		LeaseID:        100,
	}
	return &mockMVCCSvc{
		mockKVService: newMockKVService(),
		defaultKV:     dkv,
		rangeKVs:      []*kv.KeyValue{dkv, {Key: []byte("k2"), Value: []byte("v2"), CreateRevision: 1, ModRevision: 3, Version: 1}},
		rangeCount:    2,
		rangeRevision: 9,
		currentRev:    42,
	}
}

func (m *mockMVCCSvc) PutWithLease(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastLeaseID = leaseID
	if m.putLeaseErr != nil {
		return nil, m.putLeaseErr
	}
	return m.defaultKV, nil
}

func (m *mockMVCCSvc) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, _ writemode.Mode) (*kv.KeyValue, error) {
	return m.PutWithLease(ctx, key, value, leaseID)
}

func (m *mockMVCCSvc) GetRevision(_ context.Context, key string) (*kv.KeyValue, error) {
	if m.getRevErr != nil {
		return nil, m.getRevErr
	}
	return m.defaultKV, nil
}

func (m *mockMVCCSvc) GetRevisionWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return m.GetRevision(ctx, key)
}

func (m *mockMVCCSvc) GetAtRevision(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRevision = revision
	if m.getAtRevErr != nil {
		return nil, m.getAtRevErr
	}
	return m.defaultKV, nil
}

func (m *mockMVCCSvc) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
	return m.GetAtRevision(ctx, key, revision)
}

func (m *mockMVCCSvc) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRangeStart = append([]byte(nil), start...)
	m.lastRangeEnd = append([]byte(nil), end...)
	if m.rangeErr != nil {
		return nil, 0, 0, m.rangeErr
	}
	return m.rangeKVs, m.rangeCount, m.rangeRevision, nil
}

func (m *mockMVCCSvc) RangeWithConsistency(ctx context.Context, start, end []byte, _ consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*kv.KeyValue, int64, int64, error) {
	return m.Range(ctx, start, end, rangeOpts...)
}

func (m *mockMVCCSvc) Compact(_ context.Context, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCompact = revision
	return m.compactErr
}

func (m *mockMVCCSvc) CurrentRevision(_ context.Context) (int64, error) {
	if m.currentRevErr != nil {
		return 0, m.currentRevErr
	}
	return m.currentRev, nil
}

func (m *mockMVCCSvc) RestoreToRevision(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRestore = req
	if m.restoreErr != nil {
		return nil, m.restoreErr
	}
	if m.restoreResult != nil {
		return m.restoreResult, nil
	}
	return &service.RestoreRevisionResult{
		Revision: req.Revision,
		Changes: []service.RestoreKeyChange{
			{Key: "restored-key", Action: service.RestoreActionRevert, FromRevision: 5, ToRevision: 3, ValueBytes: 7},
		},
		Applied: 1,
		DryRun:  req.DryRun,
	}, nil
}

func connectMVCCInProcess(t *testing.T, svc service.MVCCKVServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithMVCC(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// PutWithLease
// ---------------------------------------------------------------------------

func TestPutWithLease_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	kvOut, err := tr.PutWithLease(context.Background(), "k1", []byte("v1"), 100)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if string(kvOut.Key) != "k1" {
		t.Errorf("key: %q", string(kvOut.Key))
	}
	if kvOut.LeaseID != 100 {
		t.Errorf("lease: %d", kvOut.LeaseID)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastLeaseID != 100 {
		t.Errorf("recorded lease: %d", svc.lastLeaseID)
	}
}

func TestPutWithLease_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.putLeaseErr = errors.New("lease expired")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetRevision
// ---------------------------------------------------------------------------

func TestGetRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	kvOut, err := tr.GetRevision(context.Background(), "k1")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if kvOut.ModRevision != 2 {
		t.Errorf("mod revision: %d", kvOut.ModRevision)
	}
}

func TestGetRevision_NotFound(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getRevErr = &service.KeyNotFoundError{Key: "missing"}
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetRevision(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Errorf("expected *transport.KeyNotFoundError, got %T", err)
	}
}

// TestGetRevision_ServiceError checks that a store failure other than a
// missing key reaches the caller as a RequestError, not as a not-found.
func TestGetRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getRevErr = errors.New("mvcc store offline")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetRevision(context.Background(), "k1")
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("want *transport.RequestError, got %T (%v)", err, err)
	}
	var knf *transport.KeyNotFoundError
	if errors.As(err, &knf) {
		t.Fatalf("a store failure was reported as key-not-found: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetAtRevision
// ---------------------------------------------------------------------------

func TestGetAtRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	kvOut, err := tr.GetAtRevision(context.Background(), "k1", 5)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if kvOut == nil {
		t.Fatal("expected non-nil kv")
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRevision != 5 {
		t.Errorf("revision: %d", svc.lastRevision)
	}
}

func TestGetAtRevision_NotFound(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getAtRevErr = &service.KeyNotFoundError{Key: "k"}
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetAtRevision(context.Background(), "k", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	var knfErr *transport.KeyNotFoundError
	if !errors.As(err, &knfErr) {
		t.Errorf("expected *transport.KeyNotFoundError, got %T", err)
	}
}

// TestGetAtRevision_Compacted: a revision below the compaction floor comes
// back as the typed refusal carrying the revision asked for, so a caller can
// tell it from a transient failure.
func TestGetAtRevision_Compacted(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getAtRevErr = &service.RevisionCompactedError{Requested: 3, Compacted: 7}
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetAtRevision(context.Background(), "k", 3)
	var compacted *transport.RevisionCompactedError
	if !errors.As(err, &compacted) {
		t.Fatalf("got %T: %v, want *transport.RevisionCompactedError", err, err)
	}
	if compacted.Revision != 3 {
		t.Errorf("Revision = %d, want 3", compacted.Revision)
	}
}

// TestGetAtRevision_OtherErrorIsNotCompacted: any other failure is not
// reported as a compaction refusal.
func TestGetAtRevision_OtherErrorIsNotCompacted(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.getAtRevErr = errors.New("disk on fire")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.GetAtRevision(context.Background(), "k", 3)
	if err == nil {
		t.Fatal("expected error")
	}
	var compacted *transport.RevisionCompactedError
	if errors.As(err, &compacted) {
		t.Fatalf("an internal failure was reported as a compaction refusal: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Range
// ---------------------------------------------------------------------------

func TestRange_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	resp, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("count: %d", resp.Count)
	}
	if len(resp.Kvs) != 2 {
		t.Errorf("kvs: %d", len(resp.Kvs))
	}
	// Revision (9) is deliberately distinct from Count (2) so this
	// assertion cannot pass by coincidence if the revision is dropped.
	if resp.Revision != 9 {
		t.Errorf("revision: %d, want 9", resp.Revision)
	}
}

func TestRange_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.rangeErr = errors.New("scan failed")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Compact
// ---------------------------------------------------------------------------

func TestCompact_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if err := tr.Compact(context.Background(), 42); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastCompact != 42 {
		t.Errorf("compact revision: %d", svc.lastCompact)
	}
}

func TestCompact_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.compactErr = errors.New("compact failed")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if err := tr.Compact(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CurrentRevision
// ---------------------------------------------------------------------------

func TestCurrentRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	rev, err := tr.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 42 {
		t.Errorf("revision: got %d, want 42", rev)
	}
}

func TestCurrentRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.currentRevErr = errors.New("backend down")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.CurrentRevision(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RestoreToRevision
// ---------------------------------------------------------------------------

func TestRestoreToRevision_Success(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Revision != 5 || result.Applied != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "restored-key" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}
	if svc.lastRestore == nil || svc.lastRestore.Prefix != "prefix" {
		t.Fatalf("expected service prefix %q, got %+v", "prefix", svc.lastRestore)
	}
}

func TestRestoreToRevision_DryRun(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if !result.DryRun {
		t.Fatal("expected dry_run=true in response")
	}
	if svc.lastRestore == nil || !svc.lastRestore.DryRun {
		t.Fatal("expected service to receive DryRun=true")
	}
}

// TestRestoreToRevision_BinaryRoundTrip proves an arbitrary byte-valued
// prefix and response key round-trip byte-for-byte through the MCP tool's
// explicit base64 (RFC 4648) argument/response encoding.
func TestRestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryPrefix := []byte{0x00, 0xff, 0xfe, 'p'}
	binaryKey := []byte{0x00, 0xff, 0xfe, 'k'}

	svc := newMockMVCCSvc()
	svc.restoreResult = &service.RestoreRevisionResult{
		Revision: 5,
		Changes: []service.RestoreKeyChange{
			{Key: string(binaryKey), Action: service.RestoreActionDelete, FromRevision: 9},
		},
		Applied: 1,
	}
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	result, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   binaryPrefix,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if svc.lastRestore == nil || svc.lastRestore.Prefix != string(binaryPrefix) {
		t.Fatalf("server received prefix %+v, want %v", svc.lastRestore, binaryPrefix)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != string(binaryKey) {
		t.Fatalf("expected round-tripped binary response key %v, got %+v", binaryKey, result.Changes)
	}
}

func TestRestoreToRevision_Keys_BinaryRoundTrip(t *testing.T) {
	binaryKey := []byte{0x00, 0xff, 0xfe, 'k'}

	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	_, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Keys:     [][]byte{binaryKey},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if svc.lastRestore == nil || len(svc.lastRestore.Keys) != 1 || svc.lastRestore.Keys[0] != string(binaryKey) {
		t.Fatalf("expected service to receive binary key %v, got %+v", binaryKey, svc.lastRestore)
	}
}

func TestRestoreToRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCSvc()
	svc.restoreErr = errors.New("backend down")
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{Revision: 1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRestoreToRevision_NilRequest(t *testing.T) {
	svc := newMockMVCCSvc()
	tr, cleanup := connectMVCCInProcess(t, svc)
	defer cleanup()

	if _, err := tr.RestoreToRevision(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

// ---------------------------------------------------------------------------
// serverKeyValueToTransport
// ---------------------------------------------------------------------------

func TestServerKeyValueToTransport(t *testing.T) {
	in := &serverKeyValue{
		Key:            []byte("k"),
		Value:          []byte("v"),
		CreateRevision: 1,
		ModRevision:    2,
		Version:        3,
		LeaseID:        4,
	}
	out := serverKeyValueToTransport(in)
	if string(out.Key) != "k" || string(out.Value) != "v" {
		t.Errorf("key/value: %s/%s", string(out.Key), string(out.Value))
	}
	if out.CreateRevision != 1 || out.ModRevision != 2 || out.Version != 3 || out.LeaseID != 4 {
		t.Errorf("metadata: %+v", out)
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*mockMVCCSvc) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}
