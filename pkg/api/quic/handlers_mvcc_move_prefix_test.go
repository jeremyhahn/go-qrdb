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

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// moverMVCCKVService is an MVCC service that can move a prefix: it
// validates the request as the service does, records it, and answers rev
// or err.
type moverMVCCKVService struct {
	mockMVCCKVService
	rev int64
	err error
	got *service.MovePrefixRequest
}

func (m *moverMVCCKVService) MovePrefix(_ context.Context, req *service.MovePrefixRequest) (int64, error) {
	if err := service.ValidateMovePrefix(req); err != nil {
		return 0, err
	}
	if m.err != nil {
		return 0, m.err
	}
	m.got = req
	return m.rev, nil
}

func movePrefixPayload(t *testing.T, req *pb.MovePrefixRequest) []byte {
	t.Helper()
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return payload
}

func TestMovePrefix_ReturnsTheRevision(t *testing.T) {
	svc := &moverMVCCKVService{rev: 42}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(svc))

	out, err := srv.handleMovePrefix(context.Background(), movePrefixPayload(t, &pb.MovePrefixRequest{
		Src: []byte("db/stage/"), Dst: []byte("db/pages/"), ExpectedKeys: 2,
		Ops:      []*pb.Op{{Type: pb.OpType_OP_PUT, Key: []byte("db/meta"), Value: []byte{0x00, 0xff}}},
		Compares: []*pb.Compare{{Key: []byte("db/meta"), Target: pb.CompareTarget_COMPARE_MOD_REVISION, TargetUnion: &pb.Compare_ModRevision{ModRevision: 7}}},
	}))
	if err != nil {
		t.Fatalf("handleMovePrefix: %v", err)
	}
	var resp pb.MovePrefixResponse
	if err := proto.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.GetRevision() != 42 {
		t.Fatalf("revision = %d, want 42", resp.GetRevision())
	}
	if svc.got == nil || svc.got.Src != "db/stage/" || svc.got.ExpectedKeys != 2 ||
		len(svc.got.Ops) != 1 || string(svc.got.Ops[0].Value) != "\x00\xff" ||
		len(svc.got.Compares) != 1 || svc.got.Compares[0].ModRevision != 7 {
		t.Fatalf("service got %+v", svc.got)
	}
}

func TestMovePrefix_CountMismatchIsAConflict(t *testing.T) {
	svc := &moverMVCCKVService{err: &service.CommandConflictError{Message: "live count 3, expected 2"}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(svc))

	_, err := srv.handleMovePrefix(context.Background(), movePrefixPayload(t, &pb.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/"), ExpectedKeys: 2}))
	var conflict *service.CommandConflictError
	if !errors.As(err, &conflict) || errclass.Classify(err) != errclass.AlreadyExists {
		t.Fatalf("err = %v (class %v), want a CommandConflictError classed AlreadyExists", err, errclass.Classify(err))
	}
	if svc.got != nil {
		t.Fatal("a refused move recorded a write")
	}
}

func TestMovePrefix_InvalidRequestIsInvalidArgument(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(&moverMVCCKVService{}))

	_, err := srv.handleMovePrefix(context.Background(), movePrefixPayload(t, &pb.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("a/b/")}))
	if errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("err = %v (class %v), want InvalidArgument", err, errclass.Classify(err))
	}
}

func TestMovePrefix_InnerWithoutMoverIsUnimplemented(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(&mockMVCCKVService{}))

	_, err := srv.handleMovePrefix(context.Background(), movePrefixPayload(t, &pb.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/")}))
	var unsupported *service.PrefixMoveUnsupportedError
	if !errors.As(err, &unsupported) || errclass.Classify(err) != errclass.Unimplemented {
		t.Fatalf("err = %v, want *service.PrefixMoveUnsupportedError classed Unimplemented", err)
	}
}

func TestMovePrefix_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithMVCC(&moverMVCCKVService{}))

	_, err := srv.handleMovePrefix(context.Background(), []byte{0xff, 0xff})
	var unmarshal *UnmarshalError
	if !errors.As(err, &unmarshal) {
		t.Fatalf("err = %v, want *UnmarshalError", err)
	}
}
