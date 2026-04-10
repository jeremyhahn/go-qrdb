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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handlePutWithLease
// ---------------------------------------------------------------------------

func TestHandlePutWithLease_Success(t *testing.T) {
	mvccSvc := &mockMVCCKVService{
		putWithLeaseFn: func(_ context.Context, key string, value []byte, leaseID int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:            []byte(key),
				Value:          value,
				LeaseID:        leaseID,
				CreateRevision: 1,
				ModRevision:    1,
				Version:        1,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.PutWithLeaseRequest{
		Key:     "lease-key",
		Value:   []byte("lease-val"),
		LeaseId: 42,
	})
	if err != nil {
		t.Fatalf("failed to marshal PutWithLeaseRequest: %v", err)
	}

	resp, err := srv.handlePutWithLease(ctx, payload)
	if err != nil {
		t.Fatalf("handlePutWithLease returned unexpected error: %v", err)
	}

	var putResp pb.PutWithLeaseResponse
	if err := proto.Unmarshal(resp, &putResp); err != nil {
		t.Fatalf("failed to unmarshal PutWithLeaseResponse: %v", err)
	}
	if string(putResp.GetKv().GetKey()) != "lease-key" {
		t.Errorf("expected key %q, got %q", "lease-key", string(putResp.GetKv().GetKey()))
	}
	if putResp.GetKv().GetLeaseId() != 42 {
		t.Errorf("expected lease_id 42, got %d", putResp.GetKv().GetLeaseId())
	}
}

func TestHandlePutWithLease_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "put with lease failed"}
	mvccSvc := &mockMVCCKVService{
		putWithLeaseFn: func(_ context.Context, _ string, _ []byte, _ int64) (*kv.KeyValue, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.PutWithLeaseRequest{
		Key:     "k",
		Value:   []byte("v"),
		LeaseId: 1,
	})
	if err != nil {
		t.Fatalf("failed to marshal PutWithLeaseRequest: %v", err)
	}

	_, err = srv.handlePutWithLease(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handlePutWithLease, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandlePutWithLease_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handlePutWithLease(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil mvcc service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "mvcc" {
		t.Errorf("expected service %q, got %q", "mvcc", snc.Service)
	}
}

func TestHandlePutWithLease_UnmarshalError(t *testing.T) {
	mvccSvc := &mockMVCCKVService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	_, err := srv.handlePutWithLease(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleGetRevision
// ---------------------------------------------------------------------------

func TestHandleGetRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, key string) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:            []byte(key),
				Value:          []byte("rev-val"),
				CreateRevision: 5,
				ModRevision:    10,
				Version:        3,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.GetRevisionRequest{Key: "rev-key"})
	if err != nil {
		t.Fatalf("failed to marshal GetRevisionRequest: %v", err)
	}

	resp, err := srv.handleGetRevision(ctx, payload)
	if err != nil {
		t.Fatalf("handleGetRevision returned unexpected error: %v", err)
	}

	var getResp pb.GetRevisionResponse
	if err := proto.Unmarshal(resp, &getResp); err != nil {
		t.Fatalf("failed to unmarshal GetRevisionResponse: %v", err)
	}
	if getResp.GetKv().GetModRevision() != 10 {
		t.Errorf("expected mod_revision 10, got %d", getResp.GetKv().GetModRevision())
	}
	if getResp.GetKv().GetVersion() != 3 {
		t.Errorf("expected version 3, got %d", getResp.GetKv().GetVersion())
	}
}

func TestHandleGetRevision_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get revision failed"}
	mvccSvc := &mockMVCCKVService{
		getRevisionFn: func(_ context.Context, _ string) (*kv.KeyValue, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.GetRevisionRequest{Key: "k"})
	if err != nil {
		t.Fatalf("failed to marshal GetRevisionRequest: %v", err)
	}

	_, err = srv.handleGetRevision(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleGetRevision, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleGetRevision_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleGetRevision(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil mvcc service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleGetAtRevision
// ---------------------------------------------------------------------------

func TestHandleGetAtRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCKVService{
		getAtRevisionFn: func(_ context.Context, key string, revision int64) (*kv.KeyValue, error) {
			return &kv.KeyValue{
				Key:         []byte(key),
				Value:       []byte("old-val"),
				ModRevision: revision,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.GetAtRevisionRequest{
		Key:      "hist-key",
		Revision: 7,
	})
	if err != nil {
		t.Fatalf("failed to marshal GetAtRevisionRequest: %v", err)
	}

	resp, err := srv.handleGetAtRevision(ctx, payload)
	if err != nil {
		t.Fatalf("handleGetAtRevision returned unexpected error: %v", err)
	}

	var getResp pb.GetAtRevisionResponse
	if err := proto.Unmarshal(resp, &getResp); err != nil {
		t.Fatalf("failed to unmarshal GetAtRevisionResponse: %v", err)
	}
	if string(getResp.GetKv().GetValue()) != "old-val" {
		t.Errorf("expected value %q, got %q", "old-val", string(getResp.GetKv().GetValue()))
	}
	if getResp.GetKv().GetModRevision() != 7 {
		t.Errorf("expected mod_revision 7, got %d", getResp.GetKv().GetModRevision())
	}
}

func TestHandleGetAtRevision_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "revision not found"}
	mvccSvc := &mockMVCCKVService{
		getAtRevisionFn: func(_ context.Context, _ string, _ int64) (*kv.KeyValue, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.GetAtRevisionRequest{
		Key:      "k",
		Revision: 1,
	})
	if err != nil {
		t.Fatalf("failed to marshal GetAtRevisionRequest: %v", err)
	}

	_, err = srv.handleGetAtRevision(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleGetAtRevision, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleGetAtRevision_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleGetAtRevision(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil mvcc service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleRange
// ---------------------------------------------------------------------------

func TestHandleRange_Success(t *testing.T) {
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			return []*kv.KeyValue{
				{Key: start, Value: []byte("v1"), ModRevision: 1},
				{Key: []byte("mid-key"), Value: []byte("v2"), ModRevision: 2},
			}, 2, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start: []byte("a"),
		End:   []byte("z"),
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	resp, err := srv.handleRange(ctx, payload)
	if err != nil {
		t.Fatalf("handleRange returned unexpected error: %v", err)
	}

	var rangeResp pb.RangeResponse
	if err := proto.Unmarshal(resp, &rangeResp); err != nil {
		t.Fatalf("failed to unmarshal RangeResponse: %v", err)
	}
	if len(rangeResp.GetKvs()) != 2 {
		t.Fatalf("expected 2 kvs, got %d", len(rangeResp.GetKvs()))
	}
	if rangeResp.GetCount() != 2 {
		t.Errorf("expected count 2, got %d", rangeResp.GetCount())
	}
}

func TestHandleRange_WithOptions(t *testing.T) {
	var capturedOpts bool
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			if len(opts) > 0 {
				capturedOpts = true
			}
			return nil, 0, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start:     []byte("a"),
		End:       []byte("z"),
		Limit:     10,
		CountOnly: true,
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	_, err = srv.handleRange(ctx, payload)
	if err != nil {
		t.Fatalf("handleRange returned unexpected error: %v", err)
	}

	if !capturedOpts {
		t.Error("expected range options to be passed to service")
	}
}

func TestHandleRange_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "range failed"}
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, _ ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			return nil, 0, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start: []byte("a"),
		End:   []byte("z"),
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	_, err = srv.handleRange(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleRange, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleRange_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleRange(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil mvcc service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleCompact
// ---------------------------------------------------------------------------

func TestHandleCompact_Success(t *testing.T) {
	var capturedRevision int64
	mvccSvc := &mockMVCCKVService{
		compactFn: func(_ context.Context, revision int64) error {
			capturedRevision = revision
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.CompactRequest{Revision: 100})
	if err != nil {
		t.Fatalf("failed to marshal CompactRequest: %v", err)
	}

	resp, err := srv.handleCompact(ctx, payload)
	if err != nil {
		t.Fatalf("handleCompact returned unexpected error: %v", err)
	}

	if capturedRevision != 100 {
		t.Errorf("expected revision 100, got %d", capturedRevision)
	}

	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleCompact_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "compact failed"}
	mvccSvc := &mockMVCCKVService{
		compactFn: func(_ context.Context, _ int64) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.CompactRequest{Revision: 1})
	if err != nil {
		t.Fatalf("failed to marshal CompactRequest: %v", err)
	}

	_, err = srv.handleCompact(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleCompact, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleCompact_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleCompact(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil mvcc service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleCurrentRevision
// ---------------------------------------------------------------------------

func TestHandleCurrentRevision_Success(t *testing.T) {
	mvccSvc := &mockMVCCKVService{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 42, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	resp, err := srv.handleCurrentRevision(ctx, nil)
	if err != nil {
		t.Fatalf("handleCurrentRevision returned unexpected error: %v", err)
	}

	var revResp pb.CurrentRevisionResponse
	if err := proto.Unmarshal(resp, &revResp); err != nil {
		t.Fatalf("failed to unmarshal CurrentRevisionResponse: %v", err)
	}
	if revResp.GetRevision() != 42 {
		t.Errorf("expected revision 42, got %d", revResp.GetRevision())
	}
}

func TestHandleCurrentRevision_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "current revision failed"}
	mvccSvc := &mockMVCCKVService{
		currentRevisionFn: func(_ context.Context) (int64, error) {
			return 0, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	_, err := srv.handleCurrentRevision(ctx, nil)
	if err == nil {
		t.Fatal("expected error from handleCurrentRevision, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleCurrentRevision_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleCurrentRevision(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil mvcc service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleRange additional option branches
// ---------------------------------------------------------------------------

func TestHandleRange_WithRevision(t *testing.T) {
	var gotOpts int
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			gotOpts = len(opts)
			return nil, 0, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start:    []byte("a"),
		End:      []byte("z"),
		Revision: 42,
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	_, err = srv.handleRange(ctx, payload)
	if err != nil {
		t.Fatalf("handleRange returned unexpected error: %v", err)
	}
	if gotOpts != 1 {
		t.Fatalf("expected 1 option (revision), got %d", gotOpts)
	}
}

func TestHandleRange_WithKeysOnly(t *testing.T) {
	var gotOpts int
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			gotOpts = len(opts)
			return nil, 0, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start:    []byte("a"),
		End:      []byte("z"),
		KeysOnly: true,
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	_, err = srv.handleRange(ctx, payload)
	if err != nil {
		t.Fatalf("handleRange returned unexpected error: %v", err)
	}
	if gotOpts != 1 {
		t.Fatalf("expected 1 option (keysOnly), got %d", gotOpts)
	}
}

func TestHandleRange_WithSort(t *testing.T) {
	var gotOpts int
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			gotOpts = len(opts)
			return nil, 0, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start:      []byte("a"),
		End:        []byte("z"),
		SortOrder:  pb.SortOrder_SORT_ASCEND,
		SortTarget: pb.SortTarget_SORT_BY_KEY,
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	_, err = srv.handleRange(ctx, payload)
	if err != nil {
		t.Fatalf("handleRange returned unexpected error: %v", err)
	}
	if gotOpts != 1 {
		t.Fatalf("expected 1 option (sort), got %d", gotOpts)
	}
}

func TestHandleRange_AllOptions(t *testing.T) {
	var gotOpts int
	mvccSvc := &mockMVCCKVService{
		rangeFn: func(_ context.Context, _, _ []byte, opts ...service.RangeOption) ([]*kv.KeyValue, int64, error) {
			gotOpts = len(opts)
			return nil, 0, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RangeRequest{
		Start:      []byte("a"),
		End:        []byte("z"),
		Limit:      10,
		Revision:   5,
		CountOnly:  true,
		KeysOnly:   true,
		SortOrder:  pb.SortOrder_SORT_DESCEND,
		SortTarget: pb.SortTarget_SORT_BY_VERSION,
	})
	if err != nil {
		t.Fatalf("failed to marshal RangeRequest: %v", err)
	}

	_, err = srv.handleRange(ctx, payload)
	if err != nil {
		t.Fatalf("handleRange returned unexpected error: %v", err)
	}
	// limit + revision + countOnly + keysOnly + sort = 5 options
	if gotOpts != 5 {
		t.Fatalf("expected 5 options, got %d", gotOpts)
	}
}

func TestHandleRange_UnmarshalError(t *testing.T) {
	mvccSvc := &mockMVCCKVService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	_, err := srv.handleRange(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Unmarshal error tests for remaining MVCC handlers
// ---------------------------------------------------------------------------

func TestHandleGetRevision_UnmarshalError(t *testing.T) {
	mvccSvc := &mockMVCCKVService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	_, err := srv.handleGetRevision(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleGetAtRevision_UnmarshalError(t *testing.T) {
	mvccSvc := &mockMVCCKVService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	_, err := srv.handleGetAtRevision(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

func TestHandleCompact_UnmarshalError(t *testing.T) {
	mvccSvc := &mockMVCCKVService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	_, err := srv.handleCompact(ctx, []byte("invalid-protobuf"))
	if err == nil {
		t.Fatal("expected error for invalid protobuf")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}
