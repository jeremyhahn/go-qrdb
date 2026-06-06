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

package quic

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// KV Put: write-mode routing
// ---------------------------------------------------------------------------

func TestHandleKVPut_WithWriteMode_RoutesToPutWithMode(t *testing.T) {
	var capturedMode writemode.Mode
	var putWithModeCalled bool

	kvSvc := &mockKVService{
		putWithModeFn: func(_ context.Context, _ string, _ []byte, mode writemode.Mode) error {
			capturedMode = mode
			putWithModeCalled = true
			return nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.PutRequest{
		Key:       "k",
		Value:     []byte("v"),
		WriteMode: pb.WriteMode_WRITE_MODE_WAL_DURABLE,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVPut(context.Background(), payload); err != nil {
		t.Fatalf("handleKVPut: %v", err)
	}

	if !putWithModeCalled {
		t.Fatal("expected PutWithMode to be called")
	}
	if capturedMode != writemode.WALDurable {
		t.Errorf("mode = %v, want %v", capturedMode, writemode.WALDurable)
	}
}

func TestHandleKVPut_NoWriteMode_RoutesToPlainPut(t *testing.T) {
	var plainPutCalled bool

	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			plainPutCalled = true
			return nil
		},
		putWithModeFn: func(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
			t.Error("PutWithMode must not be called when WriteMode is UNSPECIFIED")
			return nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.PutRequest{Key: "k", Value: []byte("v")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVPut(context.Background(), payload); err != nil {
		t.Fatalf("handleKVPut: %v", err)
	}

	if !plainPutCalled {
		t.Fatal("expected plain Put to be called")
	}
}

// ---------------------------------------------------------------------------
// KV Get: consistency routing
// ---------------------------------------------------------------------------

func TestHandleKVGet_WithConsistency_RoutesToGetWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	var getWithConsistencyCalled bool

	kvSvc := &mockKVService{
		getWithConsistencyFn: func(_ context.Context, _ string, opts consistency.ReadOptions) ([]byte, error) {
			capturedOpts = opts
			getWithConsistencyCalled = true
			return []byte("val"), nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.GetRequest{
		Key:         "k",
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVGet(context.Background(), payload); err != nil {
		t.Fatalf("handleKVGet: %v", err)
	}

	if !getWithConsistencyCalled {
		t.Fatal("expected GetWithConsistency to be called")
	}
	if capturedOpts.Consistency != consistency.Eventual {
		t.Errorf("Consistency = %v, want Eventual", capturedOpts.Consistency)
	}
	if !capturedOpts.AllowStale {
		t.Error("AllowStale should be true for Eventual")
	}
}

func TestHandleKVGet_NoConsistency_RoutesToPlainGet(t *testing.T) {
	var plainGetCalled bool

	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			plainGetCalled = true
			return []byte("v"), nil
		},
		getWithConsistencyFn: func(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
			t.Error("GetWithConsistency must not be called when ConsistencyLevel is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.GetRequest{Key: "k"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVGet(context.Background(), payload); err != nil {
		t.Fatalf("handleKVGet: %v", err)
	}

	if !plainGetCalled {
		t.Fatal("expected plain Get to be called")
	}
}

// ---------------------------------------------------------------------------
// KV Delete: write-mode routing
// ---------------------------------------------------------------------------

func TestHandleKVDelete_WithWriteMode_RoutesToDeleteWithMode(t *testing.T) {
	var capturedMode writemode.Mode
	var deleteWithModeCalled bool

	kvSvc := &mockKVService{
		deleteWithModeFn: func(_ context.Context, _ string, mode writemode.Mode) error {
			capturedMode = mode
			deleteWithModeCalled = true
			return nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.DeleteRequest{
		Key:       "k",
		WriteMode: pb.WriteMode_WRITE_MODE_BATCHED,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVDelete(context.Background(), payload); err != nil {
		t.Fatalf("handleKVDelete: %v", err)
	}

	if !deleteWithModeCalled {
		t.Fatal("expected DeleteWithMode to be called")
	}
	if capturedMode != writemode.Batched {
		t.Errorf("mode = %v, want %v", capturedMode, writemode.Batched)
	}
}

func TestHandleKVDelete_NoWriteMode_RoutesToPlainDelete(t *testing.T) {
	var plainDeleteCalled bool

	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			plainDeleteCalled = true
			return nil
		},
		deleteWithModeFn: func(_ context.Context, _ string, _ writemode.Mode) error {
			t.Error("DeleteWithMode must not be called when WriteMode is UNSPECIFIED")
			return nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.DeleteRequest{Key: "k"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVDelete(context.Background(), payload); err != nil {
		t.Fatalf("handleKVDelete: %v", err)
	}

	if !plainDeleteCalled {
		t.Fatal("expected plain Delete to be called")
	}
}

// ---------------------------------------------------------------------------
// KV List: consistency routing
// ---------------------------------------------------------------------------

func TestHandleKVList_WithConsistency_RoutesToListWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	var listWithConsistencyCalled bool

	kvSvc := &mockKVService{
		listWithConsistencyFn: func(_ context.Context, _ string, opts consistency.ReadOptions) ([]string, error) {
			capturedOpts = opts
			listWithConsistencyCalled = true
			return []string{"a", "b"}, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.ListRequest{
		Prefix:      "pref/",
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVList(context.Background(), payload); err != nil {
		t.Fatalf("handleKVList: %v", err)
	}

	if !listWithConsistencyCalled {
		t.Fatal("expected ListWithConsistency to be called")
	}
	if capturedOpts.Consistency != consistency.Strong {
		t.Errorf("Consistency = %v, want Strong", capturedOpts.Consistency)
	}
}

func TestHandleKVList_NoConsistency_RoutesToPlainList(t *testing.T) {
	var plainListCalled bool

	kvSvc := &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			plainListCalled = true
			return nil, nil
		},
		listWithConsistencyFn: func(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
			t.Error("ListWithConsistency must not be called when ConsistencyLevel is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.ListRequest{Prefix: "x/"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVList(context.Background(), payload); err != nil {
		t.Fatalf("handleKVList: %v", err)
	}

	if !plainListCalled {
		t.Fatal("expected plain List to be called")
	}
}

// ---------------------------------------------------------------------------
// KV Scan: consistency routing
// ---------------------------------------------------------------------------

func TestHandleKVScan_WithConsistency_RoutesToScanWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	var scanWithConsistencyCalled bool

	kvSvc := &mockKVService{
		scanWithConsistencyFn: func(_ context.Context, _ string, opts consistency.ReadOptions) (map[string][]byte, error) {
			capturedOpts = opts
			scanWithConsistencyCalled = true
			return map[string][]byte{"ns/x": []byte("v")}, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.ScanRequest{
		Prefix:      "ns/",
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVScan(context.Background(), payload); err != nil {
		t.Fatalf("handleKVScan: %v", err)
	}

	if !scanWithConsistencyCalled {
		t.Fatal("expected ScanWithConsistency to be called")
	}
	if capturedOpts.Consistency != consistency.Linearizable {
		t.Errorf("Consistency = %v, want Linearizable", capturedOpts.Consistency)
	}
}

func TestHandleKVScan_NoConsistency_RoutesToPlainScan(t *testing.T) {
	var plainScanCalled bool

	kvSvc := &mockKVService{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			plainScanCalled = true
			return nil, nil
		},
		scanWithConsistencyFn: func(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
			t.Error("ScanWithConsistency must not be called when ConsistencyLevel is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})

	payload, err := proto.Marshal(&pb.ScanRequest{Prefix: "ns/"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleKVScan(context.Background(), payload); err != nil {
		t.Fatalf("handleKVScan: %v", err)
	}

	if !plainScanCalled {
		t.Fatal("expected plain Scan to be called")
	}
}

// ---------------------------------------------------------------------------
// MVCC PutWithLease: write-mode routing
// ---------------------------------------------------------------------------

func TestHandlePutWithLease_WithWriteMode_RoutesToPutWithLeaseAndMode(t *testing.T) {
	var capturedMode writemode.Mode
	var putWithLeaseAndModeCalled bool

	mvccSvc := &mockMVCCKVService{
		putWithLeaseAndModeFn: func(_ context.Context, _ string, _ []byte, _ int64, mode writemode.Mode) (*kv.KeyValue, error) {
			capturedMode = mode
			putWithLeaseAndModeCalled = true
			return &kv.KeyValue{}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.PutWithLeaseRequest{
		Key:       "k",
		Value:     []byte("v"),
		LeaseId:   10,
		WriteMode: pb.WriteMode_WRITE_MODE_SYNC,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handlePutWithLease(context.Background(), payload); err != nil {
		t.Fatalf("handlePutWithLease: %v", err)
	}

	if !putWithLeaseAndModeCalled {
		t.Fatal("expected PutWithLeaseAndMode to be called")
	}
	if capturedMode != writemode.Sync {
		t.Errorf("mode = %v, want Sync", capturedMode)
	}
}

func TestHandlePutWithLease_NoWriteMode_RoutesToPlainPutWithLease(t *testing.T) {
	var plainCalled bool

	mvccSvc := &mockMVCCKVService{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			plainCalled = true
			return &kv.KeyValue{}, nil
		},
		putWithLeaseAndModeFn: func(_ context.Context, _ string, _ []byte, _ int64, _ writemode.Mode) (*kv.KeyValue, error) {
			t.Error("PutWithLeaseAndMode must not be called when WriteMode is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.PutWithLeaseRequest{
		Key:     "k",
		Value:   []byte("v"),
		LeaseId: 5,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handlePutWithLease(context.Background(), payload); err != nil {
		t.Fatalf("handlePutWithLease: %v", err)
	}

	if !plainCalled {
		t.Fatal("expected plain PutWithLease to be called")
	}
}

// ---------------------------------------------------------------------------
// MVCC GetRevision: consistency routing
// ---------------------------------------------------------------------------

func TestHandleGetRevision_WithConsistency_RoutesToGetRevisionWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	var getRevWithConsistencyCalled bool

	mvccSvc := &mockMVCCKVService{
		getRevisionWithConsistencyFn: func(_ context.Context, _ string, opts consistency.ReadOptions) (*kv.KeyValue, error) {
			capturedOpts = opts
			getRevWithConsistencyCalled = true
			return &kv.KeyValue{ModRevision: 7}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.GetRevisionRequest{
		Key:         "k",
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleGetRevision(context.Background(), payload); err != nil {
		t.Fatalf("handleGetRevision: %v", err)
	}

	if !getRevWithConsistencyCalled {
		t.Fatal("expected GetRevisionWithConsistency to be called")
	}
	if capturedOpts.Consistency != consistency.Strong {
		t.Errorf("Consistency = %v, want Strong", capturedOpts.Consistency)
	}
}

func TestHandleGetRevision_NoConsistency_RoutesToPlainGetRevision(t *testing.T) {
	var plainCalled bool

	mvccSvc := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			plainCalled = true
			return &kv.KeyValue{}, nil
		},
		getRevisionWithConsistencyFn: func(_ context.Context, _ string, _ consistency.ReadOptions) (*kv.KeyValue, error) {
			t.Error("GetRevisionWithConsistency must not be called when ConsistencyLevel is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.GetRevisionRequest{Key: "k"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleGetRevision(context.Background(), payload); err != nil {
		t.Fatalf("handleGetRevision: %v", err)
	}

	if !plainCalled {
		t.Fatal("expected plain GetRevision to be called")
	}
}

// ---------------------------------------------------------------------------
// MVCC GetAtRevision: consistency routing
// ---------------------------------------------------------------------------

func TestHandleGetAtRevision_WithConsistency_RoutesToGetAtRevisionWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	var getAtRevWithConsistencyCalled bool

	mvccSvc := &mockMVCCKVService{
		getAtRevisionWithConsistencyFn: func(_ context.Context, _ string, _ int64, opts consistency.ReadOptions) (*kv.KeyValue, error) {
			capturedOpts = opts
			getAtRevWithConsistencyCalled = true
			return &kv.KeyValue{ModRevision: 3}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.GetAtRevisionRequest{
		Key:         "k",
		Revision:    3,
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleGetAtRevision(context.Background(), payload); err != nil {
		t.Fatalf("handleGetAtRevision: %v", err)
	}

	if !getAtRevWithConsistencyCalled {
		t.Fatal("expected GetAtRevisionWithConsistency to be called")
	}
	if capturedOpts.Consistency != consistency.Eventual {
		t.Errorf("Consistency = %v, want Eventual", capturedOpts.Consistency)
	}
	if !capturedOpts.AllowStale {
		t.Error("AllowStale should be true for Eventual")
	}
}

func TestHandleGetAtRevision_NoConsistency_RoutesToPlainGetAtRevision(t *testing.T) {
	var plainCalled bool

	mvccSvc := &mockMVCCKVService{
		getAtRevisionFn: func(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
			plainCalled = true
			return &kv.KeyValue{}, nil
		},
		getAtRevisionWithConsistencyFn: func(_ context.Context, _ string, _ int64, _ consistency.ReadOptions) (*kv.KeyValue, error) {
			t.Error("GetAtRevisionWithConsistency must not be called when ConsistencyLevel is UNSPECIFIED")
			return nil, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.GetAtRevisionRequest{Key: "k", Revision: 2})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleGetAtRevision(context.Background(), payload); err != nil {
		t.Fatalf("handleGetAtRevision: %v", err)
	}

	if !plainCalled {
		t.Fatal("expected plain GetAtRevision to be called")
	}
}

// ---------------------------------------------------------------------------
// MVCC Range: consistency routing
// ---------------------------------------------------------------------------

func TestHandleRange_WithConsistency_RoutesToRangeWithConsistency(t *testing.T) {
	var capturedOpts consistency.ReadOptions
	var rangeWithConsistencyCalled bool

	mvccSvc := &mockMVCCKVService{
		rangeWithConsistencyFn: func(_ context.Context, _, _ []byte, opts consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			capturedOpts = opts
			rangeWithConsistencyCalled = true
			return []*kv.KeyValue{{Key: []byte("a"), Value: []byte("1")}}, 1, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start:       []byte("a"),
		End:         []byte("z"),
		Consistency: pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleRange(context.Background(), payload); err != nil {
		t.Fatalf("handleRange: %v", err)
	}

	if !rangeWithConsistencyCalled {
		t.Fatal("expected RangeWithConsistency to be called")
	}
	if capturedOpts.Consistency != consistency.Linearizable {
		t.Errorf("Consistency = %v, want Linearizable", capturedOpts.Consistency)
	}
}

func TestHandleRange_NoConsistency_RoutesToPlainRange(t *testing.T) {
	var plainCalled bool

	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			plainCalled = true
			return nil, 0, nil
		},
		rangeWithConsistencyFn: func(_ context.Context, _, _ []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			t.Error("RangeWithConsistency must not be called when ConsistencyLevel is UNSPECIFIED")
			return nil, 0, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	payload, err := proto.Marshal(&pb.RangeRequest{Start: []byte("a"), End: []byte("z")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if _, err := srv.handleRange(context.Background(), payload); err != nil {
		t.Fatalf("handleRange: %v", err)
	}

	if !plainCalled {
		t.Fatal("expected plain Range to be called")
	}
}

// ---------------------------------------------------------------------------
// readOptsFromWire / writeModeFromWire helper unit tests
// ---------------------------------------------------------------------------

func TestReadOptsFromWire_Unspecified_ReturnsFalse(t *testing.T) {
	_, ok := readOptsFromWire(pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED)
	if ok {
		t.Error("expected false for UNSPECIFIED")
	}
}

func TestReadOptsFromWire_Strong(t *testing.T) {
	opts, ok := readOptsFromWire(pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG)
	if !ok {
		t.Fatal("expected true for STRONG")
	}
	if opts.Consistency != consistency.Strong {
		t.Errorf("Consistency = %v, want Strong", opts.Consistency)
	}
	if opts.AllowStale {
		t.Error("AllowStale should be false for Strong")
	}
}

func TestReadOptsFromWire_Linearizable(t *testing.T) {
	opts, ok := readOptsFromWire(pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE)
	if !ok {
		t.Fatal("expected true for LINEARIZABLE")
	}
	if opts.Consistency != consistency.Linearizable {
		t.Errorf("Consistency = %v, want Linearizable", opts.Consistency)
	}
}

func TestReadOptsFromWire_Eventual_SetsAllowStale(t *testing.T) {
	opts, ok := readOptsFromWire(pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL)
	if !ok {
		t.Fatal("expected true for EVENTUAL")
	}
	if opts.Consistency != consistency.Eventual {
		t.Errorf("Consistency = %v, want Eventual", opts.Consistency)
	}
	if !opts.AllowStale {
		t.Error("AllowStale should be true for Eventual")
	}
}

func TestWriteModeFromWire_Unspecified_ReturnsFalse(t *testing.T) {
	_, ok := writeModeFromWire(pb.WriteMode_WRITE_MODE_UNSPECIFIED)
	if ok {
		t.Error("expected false for UNSPECIFIED")
	}
}

func TestWriteModeFromWire_Sync(t *testing.T) {
	mode, ok := writeModeFromWire(pb.WriteMode_WRITE_MODE_SYNC)
	if !ok {
		t.Fatal("expected true for SYNC")
	}
	if mode != writemode.Sync {
		t.Errorf("mode = %v, want Sync", mode)
	}
}

func TestWriteModeFromWire_WALDurable(t *testing.T) {
	mode, ok := writeModeFromWire(pb.WriteMode_WRITE_MODE_WAL_DURABLE)
	if !ok {
		t.Fatal("expected true for WAL_DURABLE")
	}
	if mode != writemode.WALDurable {
		t.Errorf("mode = %v, want WALDurable", mode)
	}
}

func TestWriteModeFromWire_Batched(t *testing.T) {
	mode, ok := writeModeFromWire(pb.WriteMode_WRITE_MODE_BATCHED)
	if !ok {
		t.Fatal("expected true for BATCHED")
	}
	if mode != writemode.Batched {
		t.Errorf("mode = %v, want Batched", mode)
	}
}
