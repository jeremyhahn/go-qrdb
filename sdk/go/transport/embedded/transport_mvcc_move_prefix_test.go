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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// moverMVCCService wraps mockMVCCService with a PrefixMover that validates
// the request as the service does, records it, and answers rev or err.
type moverMVCCService struct {
	*mockMVCCService
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
	svc := &moverMVCCService{mockMVCCService: newMockMVCCService(), rev: 42}
	tr := newExtTransport(t, WithMVCCService(svc))

	rev, err := tr.MovePrefix(context.Background(), &transport.MovePrefixRequest{
		Src: []byte("db/stage/"), Dst: []byte("db/pages/"), ExpectedKeys: 2,
		Ops:      []transport.Op{{Type: transport.OpPut, Key: []byte("db/meta"), Value: []byte{0x00, 0xff}}},
		Compares: []transport.Compare{{Key: []byte("db/meta"), Target: transport.CompareModRevision, Result: transport.CompareEqual, ModRevision: 7}},
	})
	if err != nil {
		t.Fatalf("MovePrefix: %v", err)
	}
	if rev != 42 {
		t.Fatalf("revision = %d, want 42", rev)
	}
	if svc.got == nil || svc.got.Src != "db/stage/" || svc.got.Dst != "db/pages/" || svc.got.ExpectedKeys != 2 ||
		len(svc.got.Ops) != 1 || svc.got.Ops[0].Type != service.OpPut || string(svc.got.Ops[0].Value) != "\x00\xff" ||
		len(svc.got.Compares) != 1 || svc.got.Compares[0].Target != service.CompareModRevision || svc.got.Compares[0].ModRevision != 7 {
		t.Fatalf("service got %+v", svc.got)
	}
}

func TestMovePrefix_DispatchReturnsTheRevision(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(&moverMVCCService{mockMVCCService: newMockMVCCService(), rev: 9}))

	var rev int64
	if err := handleMovePrefix(context.Background(), tr, &transport.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/"), ExpectedKeys: 1}, &rev); err != nil {
		t.Fatalf("handleMovePrefix: %v", err)
	}
	if rev != 9 {
		t.Fatalf("revision = %d, want 9", rev)
	}
	var invalid *InvalidRequestError
	if err := handleMovePrefix(context.Background(), tr, "not a request", &rev); !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *InvalidRequestError", err)
	}
}

func TestMovePrefix_CountMismatchIsAConflict(t *testing.T) {
	svc := &moverMVCCService{mockMVCCService: newMockMVCCService(), err: &service.CommandConflictError{Message: "live count 3, expected 2"}}
	tr := newExtTransport(t, WithMVCCService(svc))

	_, err := tr.MovePrefix(context.Background(), &transport.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/"), ExpectedKeys: 2})
	var conflict *service.CommandConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v (%T), want a CommandConflictError", err, err)
	}
	if svc.got != nil {
		t.Fatal("a refused move recorded a write")
	}
}

func TestMovePrefix_InvalidRequestIsRefused(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(&moverMVCCService{mockMVCCService: newMockMVCCService()}))

	_, err := tr.MovePrefix(context.Background(), &transport.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("a/b/")})
	var invalid *service.PrefixMoveInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want *service.PrefixMoveInvalidError", err, err)
	}
}

func TestMovePrefix_ServiceWithoutMoverIsUnsupported(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(newMockMVCCService()))

	_, err := tr.MovePrefix(context.Background(), &transport.MovePrefixRequest{Src: []byte("a/"), Dst: []byte("b/")})
	var unsupported *service.PrefixMoveUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v (%T), want *service.PrefixMoveUnsupportedError", err, err)
	}
}

func TestMovePrefix_NilRequest(t *testing.T) {
	tr := newExtTransport(t, WithMVCCService(&moverMVCCService{mockMVCCService: newMockMVCCService()}))

	_, err := tr.MovePrefix(context.Background(), nil)
	var nilReq *transport.NilRequestError
	if !errors.As(err, &nilReq) {
		t.Fatalf("err = %v, want *transport.NilRequestError", err)
	}
}
