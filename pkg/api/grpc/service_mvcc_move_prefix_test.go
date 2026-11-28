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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// moverMVCCService is an MVCC service that can move a prefix: it validates
// the request as the service does, records it, and answers rev or err.
type moverMVCCService struct {
	mockMVCCService
	rev int64
	err error
	got *service.MovePrefixRequest
}

func (m *moverMVCCService) MovePrefix(_ context.Context, req *service.MovePrefixRequest) (int64, error) {
	if err := service.ValidateMovePrefix(req); err != nil {
		return 0, err
	}
	if m.err != nil {
		return 0, m.err
	}
	m.got = req
	return m.rev, nil
}

func TestMovePrefix_ReturnsTheRevision(t *testing.T) {
	svc := &moverMVCCService{rev: 42}
	client, cleanup := startTestServerFull(t, WithMVCC(svc))
	defer cleanup()

	resp, err := client.MovePrefix(context.Background(), &pb.MovePrefixRequest{
		Src: []byte("db/stage/"), Dst: []byte("db/pages/"), ExpectedKeys: 2,
		Ops:      []*pb.Op{{Type: pb.OpType_OP_PUT, Key: []byte("db/meta"), Value: []byte{0x00, 0xff}}},
		Compares: []*pb.Compare{{Key: []byte("db/meta"), Target: pb.CompareTarget_COMPARE_MOD_REVISION, Result: pb.CompareResult_COMPARE_EQUAL, TargetUnion: &pb.Compare_ModRevision{ModRevision: 7}}},
	})
	if err != nil {
		t.Fatalf("MovePrefix: %v", err)
	}
	if resp.GetRevision() != 42 {
		t.Fatalf("revision = %d, want 42", resp.GetRevision())
	}
	if svc.got == nil || svc.got.Src != "db/stage/" || svc.got.Dst != "db/pages/" || svc.got.ExpectedKeys != 2 {
		t.Fatalf("service got %+v", svc.got)
	}
	if len(svc.got.Ops) != 1 || svc.got.Ops[0].Type != service.OpPut || string(svc.got.Ops[0].Value) != "\x00\xff" {
		t.Fatalf("ops = %+v", svc.got.Ops)
	}
	if len(svc.got.Compares) != 1 || svc.got.Compares[0].ModRevision != 7 || svc.got.Compares[0].Target != service.CompareModRevision {
		t.Fatalf("compares = %+v", svc.got.Compares)
	}
}

func TestMovePrefix_CountMismatchIsAConflict(t *testing.T) {
	svc := &moverMVCCService{err: &service.CommandConflictError{Message: "live count 3 under src, expected 2"}}
	client, cleanup := startTestServerFull(t, WithMVCC(svc))
	defer cleanup()

	_, err := client.MovePrefix(context.Background(), &pb.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/"), ExpectedKeys: 2})
	if st, _ := status.FromError(err); st.Code() != codes.AlreadyExists {
		t.Fatalf("code = %s (%v), want AlreadyExists", st.Code(), err)
	}
	if svc.got != nil {
		t.Fatal("a refused move recorded a write")
	}
}

func TestMovePrefix_InvalidRequestIsInvalidArgument(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithMVCC(&moverMVCCService{}))
	defer cleanup()

	_, err := client.MovePrefix(context.Background(), &pb.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("a/b/")})
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %s (%v), want InvalidArgument", st.Code(), err)
	}
}

func TestMovePrefix_InnerWithoutMoverIsUnimplemented(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithMVCC(&mockMVCCService{}))
	defer cleanup()

	_, err := client.MovePrefix(context.Background(), &pb.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/")})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s (%v), want Unimplemented", st.Code(), err)
	}
}
