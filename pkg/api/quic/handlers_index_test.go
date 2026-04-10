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
	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// handleIndexCreate
// ---------------------------------------------------------------------------

func TestHandleIndexCreate_Success(t *testing.T) {
	var capturedName, capturedField string
	var capturedCfg index.IndexConfig

	indexSvc := &mockIndexService{
		createIndexFn: func(_ context.Context, name, fieldName string, config index.IndexConfig) error {
			capturedName = name
			capturedField = fieldName
			capturedCfg = config
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexCreateRequest{
		Name:      "email_idx",
		FieldName: "email",
		Unique:    true,
		Sparse:    false,
	})
	if err != nil {
		t.Fatalf("failed to marshal IndexCreateRequest: %v", err)
	}

	resp, err := srv.handleIndexCreate(ctx, payload)
	if err != nil {
		t.Fatalf("handleIndexCreate returned unexpected error: %v", err)
	}

	if capturedName != "email_idx" {
		t.Errorf("expected name %q, got %q", "email_idx", capturedName)
	}
	if capturedField != "email" {
		t.Errorf("expected field %q, got %q", "email", capturedField)
	}
	if !capturedCfg.Unique {
		t.Error("expected unique to be true")
	}
	if capturedCfg.Sparse {
		t.Error("expected sparse to be false")
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleIndexCreate_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "index create failed"}
	indexSvc := &mockIndexService{
		createIndexFn: func(_ context.Context, _, _ string, _ index.IndexConfig) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexCreateRequest{
		Name:      "idx",
		FieldName: "f",
	})
	if err != nil {
		t.Fatalf("failed to marshal IndexCreateRequest: %v", err)
	}

	_, err = srv.handleIndexCreate(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleIndexCreate, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleIndexCreate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleIndexCreate(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil index service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if snc.Service != "index" {
		t.Errorf("expected service %q, got %q", "index", snc.Service)
	}
}

func TestHandleIndexCreate_UnmarshalError(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	_, err := srv.handleIndexCreate(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleIndexDrop
// ---------------------------------------------------------------------------

func TestHandleIndexDrop_Success(t *testing.T) {
	var capturedName string
	indexSvc := &mockIndexService{
		dropIndexFn: func(_ context.Context, name string) error {
			capturedName = name
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexDropRequest{Name: "old_idx"})
	if err != nil {
		t.Fatalf("failed to marshal IndexDropRequest: %v", err)
	}

	resp, err := srv.handleIndexDrop(ctx, payload)
	if err != nil {
		t.Fatalf("handleIndexDrop returned unexpected error: %v", err)
	}

	if capturedName != "old_idx" {
		t.Errorf("expected name %q, got %q", "old_idx", capturedName)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty response, got %d bytes", len(resp))
	}
}

func TestHandleIndexDrop_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "index drop failed"}
	indexSvc := &mockIndexService{
		dropIndexFn: func(_ context.Context, _ string) error {
			return svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexDropRequest{Name: "x"})
	if err != nil {
		t.Fatalf("failed to marshal IndexDropRequest: %v", err)
	}

	_, err = srv.handleIndexDrop(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleIndexDrop, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleIndexDrop_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleIndexDrop(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil index service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleIndexQuery
// ---------------------------------------------------------------------------

func TestHandleIndexQuery_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		queryFn: func(_ context.Context, indexName string, value []byte) ([][]byte, error) {
			if indexName != "name_idx" {
				t.Errorf("expected index %q, got %q", "name_idx", indexName)
			}
			if string(value) != "alice" {
				t.Errorf("expected value %q, got %q", "alice", string(value))
			}
			return [][]byte{[]byte("1"), []byte("2"), []byte("3")}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexQueryRequest{
		IndexName: "name_idx",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("failed to marshal IndexQueryRequest: %v", err)
	}

	resp, err := srv.handleIndexQuery(ctx, payload)
	if err != nil {
		t.Fatalf("handleIndexQuery returned unexpected error: %v", err)
	}

	var queryResp pb.IndexQueryResponse
	if err := proto.Unmarshal(resp, &queryResp); err != nil {
		t.Fatalf("failed to unmarshal IndexQueryResponse: %v", err)
	}
	if len(queryResp.GetKeys()) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(queryResp.GetKeys()))
	}
	if string(queryResp.GetKeys()[0]) != "1" {
		t.Errorf("expected first key %q, got %q", "1", string(queryResp.GetKeys()[0]))
	}
}

func TestHandleIndexQuery_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "index query failed"}
	indexSvc := &mockIndexService{
		queryFn: func(_ context.Context, _ string, _ []byte) ([][]byte, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexQueryRequest{
		IndexName: "idx",
		Value:     []byte("x"),
	})
	if err != nil {
		t.Fatalf("failed to marshal IndexQueryRequest: %v", err)
	}

	_, err = srv.handleIndexQuery(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleIndexQuery, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleIndexQuery_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleIndexQuery(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil index service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleIndexScan
// ---------------------------------------------------------------------------

func TestHandleIndexScan_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		scanFn: func(_ context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
			if indexName != "age_idx" {
				t.Errorf("expected index %q, got %q", "age_idx", indexName)
			}
			return map[string][][]byte{
				"20": {[]byte("10"), []byte("20")},
				"30": {[]byte("30")},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexScanRequest{
		IndexName: "age_idx",
		Start:     []byte("20"),
		End:       []byte("40"),
	})
	if err != nil {
		t.Fatalf("failed to marshal IndexScanRequest: %v", err)
	}

	resp, err := srv.handleIndexScan(ctx, payload)
	if err != nil {
		t.Fatalf("handleIndexScan returned unexpected error: %v", err)
	}

	var scanResp pb.IndexScanResponse
	if err := proto.Unmarshal(resp, &scanResp); err != nil {
		t.Fatalf("failed to unmarshal IndexScanResponse: %v", err)
	}
	if len(scanResp.GetEntries()) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(scanResp.GetEntries()))
	}
}

func TestHandleIndexScan_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "index scan failed"}
	indexSvc := &mockIndexService{
		scanFn: func(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.IndexScanRequest{
		IndexName: "idx",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err != nil {
		t.Fatalf("failed to marshal IndexScanRequest: %v", err)
	}

	_, err = srv.handleIndexScan(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleIndexScan, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleIndexScan_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleIndexScan(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil index service, got nil")
	}

	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleIndexScan_UnmarshalError(t *testing.T) {
	indexSvc := &mockIndexService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithIndex(indexSvc))
	ctx := context.Background()

	_, err := srv.handleIndexScan(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}
