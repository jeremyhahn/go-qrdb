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
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandleRestoreToRevision_Success(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCKVService{
		restoreToRevisionFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{
				Revision: 5,
				Changes: []service.RestoreKeyChange{
					{Key: "a", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5, ValueBytes: 3},
				},
				Applied: 1,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := srv.handleRestoreToRevision(ctx, payload)
	if err != nil {
		t.Fatalf("handleRestoreToRevision returned unexpected error: %v", err)
	}

	var pbResp pb.RestoreRevisionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if pbResp.GetRevision() != 5 || pbResp.GetApplied() != 1 {
		t.Fatalf("unexpected response: %+v", &pbResp)
	}
	if len(pbResp.GetChanges()) != 1 || !bytes.Equal(pbResp.GetChanges()[0].GetKey(), []byte("a")) {
		t.Fatalf("unexpected changes: %+v", pbResp.GetChanges())
	}
	if captured == nil || captured.Prefix != "prefix" {
		t.Fatalf("expected service prefix %q, got %+v", "prefix", captured)
	}
}

func TestHandleRestoreToRevision_DryRun(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCKVService{
		restoreToRevisionFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{Revision: 5, DryRun: true}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := srv.handleRestoreToRevision(ctx, payload)
	if err != nil {
		t.Fatalf("handleRestoreToRevision returned unexpected error: %v", err)
	}
	if captured == nil || !captured.DryRun {
		t.Fatal("expected service to receive DryRun=true")
	}

	var pbResp pb.RestoreRevisionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !pbResp.GetDryRun() {
		t.Fatal("expected dry_run=true in response")
	}
}

func TestHandleRestoreToRevision_ForceWithConflicts(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	mvccSvc := &mockMVCCKVService{
		restoreToRevisionFn: func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			captured = req
			return &service.RestoreRevisionResult{
				Revision: 5,
				Conflicts: []service.RestoreKeyChange{
					{Key: "b", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
				},
				Applied:    1,
				Conflicted: 1,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RestoreRevisionRequest{
		Revision: 5,
		Keys:     [][]byte{[]byte("a"), []byte("b")},
		Force:    true,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := srv.handleRestoreToRevision(ctx, payload)
	if err != nil {
		t.Fatalf("handleRestoreToRevision returned unexpected error: %v", err)
	}
	if captured == nil || !captured.Force {
		t.Fatal("expected service to receive Force=true")
	}
	if len(captured.Keys) != 2 || captured.Keys[0] != "a" || captured.Keys[1] != "b" {
		t.Fatalf("expected service keys [a b], got %v", captured.Keys)
	}

	var pbResp pb.RestoreRevisionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if pbResp.GetConflicted() != 1 || len(pbResp.GetConflicts()) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", &pbResp)
	}
}

func TestHandleRestoreToRevision_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "restore failed"}
	mvccSvc := &mockMVCCKVService{
		restoreToRevisionFn: func(_ context.Context, _ *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))
	ctx := context.Background()

	payload, err := proto.Marshal(&pb.RestoreRevisionRequest{Revision: 1, Prefix: []byte("k")})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	_, err = srv.handleRestoreToRevision(ctx, payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected the service error to propagate unwrapped, got %v", err)
	}
}

func TestHandleRestoreToRevision_UnmarshalError(t *testing.T) {
	mvccSvc := &mockMVCCKVService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(mvccSvc))

	_, err := srv.handleRestoreToRevision(context.Background(), []byte{0xff, 0xff, 0xff})
	var unmarshalErr *UnmarshalError
	if !errors.As(err, &unmarshalErr) {
		t.Fatalf("expected *UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mapServiceError: restore guard errors
// ---------------------------------------------------------------------------

func TestMapServiceError_MVCCBackendRequiredError(t *testing.T) {
	err := &service.MVCCBackendRequiredError{Err: errors.New("unsupported query type")}
	statusCode, _ := mapServiceError(err)
	if statusCode != wire.StatusUnimplemented {
		t.Fatalf("expected StatusUnimplemented (%d), got %d", wire.StatusUnimplemented, statusCode)
	}
}

func TestMapServiceError_RestoreChangeIndexUnavailableError(t *testing.T) {
	err := &service.RestoreChangeIndexUnavailableError{Err: errors.New("no index")}
	statusCode, _ := mapServiceError(err)
	if statusCode != wire.StatusUnimplemented {
		t.Fatalf("expected StatusUnimplemented (%d), got %d", wire.StatusUnimplemented, statusCode)
	}
}

func TestMapServiceError_RestoreInternalKeyError(t *testing.T) {
	err := &service.RestoreInternalKeyError{Prefix: "__sys:"}
	statusCode, _ := mapServiceError(err)
	if statusCode != wire.StatusPermissionDenied {
		t.Fatalf("expected StatusPermissionDenied (%d), got %d", wire.StatusPermissionDenied, statusCode)
	}
}

func TestMapServiceError_RestoreScopeEmptyError(t *testing.T) {
	err := &service.RestoreScopeEmptyError{}
	statusCode, _ := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
	}
}

func TestMapServiceError_RestoreRevisionFutureError(t *testing.T) {
	err := &service.RestoreRevisionFutureError{Requested: 99, Current: 5}
	statusCode, _ := mapServiceError(err)
	if statusCode != wire.StatusError {
		t.Fatalf("expected StatusError (%d), got %d", wire.StatusError, statusCode)
	}
}
