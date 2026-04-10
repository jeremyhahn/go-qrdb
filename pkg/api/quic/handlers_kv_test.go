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
)

// ---------------------------------------------------------------------------
// handleKVPut
// ---------------------------------------------------------------------------

func TestHandleKVPut_Success(t *testing.T) {
	var capturedKey string
	var capturedValue []byte

	kvSvc := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			capturedKey = key
			capturedValue = value
			return nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.PutRequest{
		Key:   "test-key",
		Value: []byte("test-value"),
	})
	if err != nil {
		t.Fatalf("failed to marshal PutRequest: %v", err)
	}

	resp, err := srv.handleKVPut(ctx, payload)
	if err != nil {
		t.Fatalf("handleKVPut returned unexpected error: %v", err)
	}

	if capturedKey != "test-key" {
		t.Errorf("expected captured key %q, got %q", "test-key", capturedKey)
	}
	if string(capturedValue) != "test-value" {
		t.Errorf("expected captured value %q, got %q", "test-value", string(capturedValue))
	}

	var putResp pb.PutResponse
	if err := proto.Unmarshal(resp, &putResp); err != nil {
		t.Fatalf("failed to unmarshal PutResponse: %v", err)
	}
}

func TestHandleKVPut_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "put failed"}
	kvSvc := &mockKVService{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return svcErr
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.PutRequest{
		Key:   "k",
		Value: []byte("v"),
	})
	if err != nil {
		t.Fatalf("failed to marshal PutRequest: %v", err)
	}

	_, err = srv.handleKVPut(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleKVPut, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
	if te.msg != "put failed" {
		t.Errorf("expected error message %q, got %q", "put failed", te.msg)
	}
}

func TestHandleKVPut_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleKVPut(ctx, []byte{0xff, 0xfe, 0xfd})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleKVGet
// ---------------------------------------------------------------------------

func TestHandleKVGet_Success(t *testing.T) {
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key != "mykey" {
				t.Errorf("expected key %q, got %q", "mykey", key)
			}
			return []byte("myvalue"), nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.GetRequest{Key: "mykey"})
	if err != nil {
		t.Fatalf("failed to marshal GetRequest: %v", err)
	}

	resp, err := srv.handleKVGet(ctx, payload)
	if err != nil {
		t.Fatalf("handleKVGet returned unexpected error: %v", err)
	}

	var getResp pb.GetResponse
	if err := proto.Unmarshal(resp, &getResp); err != nil {
		t.Fatalf("failed to unmarshal GetResponse: %v", err)
	}
	if string(getResp.GetValue()) != "myvalue" {
		t.Errorf("expected value %q, got %q", "myvalue", string(getResp.GetValue()))
	}
}

func TestHandleKVGet_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get failed"}
	kvSvc := &mockKVService{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.GetRequest{Key: "k"})
	if err != nil {
		t.Fatalf("failed to marshal GetRequest: %v", err)
	}

	_, err = srv.handleKVGet(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleKVGet, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleKVGet_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleKVGet(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleKVDelete
// ---------------------------------------------------------------------------

func TestHandleKVDelete_Success(t *testing.T) {
	var capturedKey string
	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, key string) error {
			capturedKey = key
			return nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.DeleteRequest{Key: "del-key"})
	if err != nil {
		t.Fatalf("failed to marshal DeleteRequest: %v", err)
	}

	resp, err := srv.handleKVDelete(ctx, payload)
	if err != nil {
		t.Fatalf("handleKVDelete returned unexpected error: %v", err)
	}

	if capturedKey != "del-key" {
		t.Errorf("expected captured key %q, got %q", "del-key", capturedKey)
	}

	var delResp pb.DeleteResponse
	if err := proto.Unmarshal(resp, &delResp); err != nil {
		t.Fatalf("failed to unmarshal DeleteResponse: %v", err)
	}
}

func TestHandleKVDelete_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "delete failed"}
	kvSvc := &mockKVService{
		deleteFn: func(_ context.Context, _ string) error {
			return svcErr
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.DeleteRequest{Key: "k"})
	if err != nil {
		t.Fatalf("failed to marshal DeleteRequest: %v", err)
	}

	_, err = srv.handleKVDelete(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleKVDelete, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleKVDelete_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleKVDelete(ctx, []byte{0xff})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleKVList
// ---------------------------------------------------------------------------

func TestHandleKVList_Success(t *testing.T) {
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			if prefix != "app/" {
				t.Errorf("expected prefix %q, got %q", "app/", prefix)
			}
			return []string{"app/a", "app/b", "app/c"}, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.ListRequest{Prefix: "app/"})
	if err != nil {
		t.Fatalf("failed to marshal ListRequest: %v", err)
	}

	resp, err := srv.handleKVList(ctx, payload)
	if err != nil {
		t.Fatalf("handleKVList returned unexpected error: %v", err)
	}

	var listResp pb.ListResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("failed to unmarshal ListResponse: %v", err)
	}
	if len(listResp.GetKeys()) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(listResp.GetKeys()))
	}
	if listResp.GetKeys()[0] != "app/a" {
		t.Errorf("expected first key %q, got %q", "app/a", listResp.GetKeys()[0])
	}
}

func TestHandleKVList_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list failed"}
	kvSvc := &mockKVService{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.ListRequest{Prefix: "x"})
	if err != nil {
		t.Fatalf("failed to marshal ListRequest: %v", err)
	}

	_, err = srv.handleKVList(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleKVList, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleKVList_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleKVList(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleKVScan
// ---------------------------------------------------------------------------

func TestHandleKVScan_Success(t *testing.T) {
	kvSvc := &mockKVService{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			if prefix != "ns/" {
				t.Errorf("expected prefix %q, got %q", "ns/", prefix)
			}
			return map[string][]byte{
				"ns/x": []byte("val-x"),
				"ns/y": []byte("val-y"),
			}, nil
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.ScanRequest{Prefix: "ns/"})
	if err != nil {
		t.Fatalf("failed to marshal ScanRequest: %v", err)
	}

	resp, err := srv.handleKVScan(ctx, payload)
	if err != nil {
		t.Fatalf("handleKVScan returned unexpected error: %v", err)
	}

	var scanResp pb.ScanResponse
	if err := proto.Unmarshal(resp, &scanResp); err != nil {
		t.Fatalf("failed to unmarshal ScanResponse: %v", err)
	}
	if len(scanResp.GetPairs()) != 2 {
		t.Fatalf("expected 2 pairs, got %d", len(scanResp.GetPairs()))
	}
}

func TestHandleKVScan_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "scan failed"}
	kvSvc := &mockKVService{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(kvSvc, &mockAdminService{})
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.ScanRequest{Prefix: "x"})
	if err != nil {
		t.Fatalf("failed to marshal ScanRequest: %v", err)
	}

	_, err = srv.handleKVScan(ctx, payload)
	if err == nil {
		t.Fatal("expected error from handleKVScan, got nil")
	}

	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T: %v", err, err)
	}
}

func TestHandleKVScan_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	ctx := context.Background()

	_, err := srv.handleKVScan(ctx, []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error for invalid payload, got nil")
	}

	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}
