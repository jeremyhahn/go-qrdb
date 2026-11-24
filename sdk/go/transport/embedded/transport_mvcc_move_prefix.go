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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MovePrefix moves every live key under req.Src to req.Dst in one revision
// with req.Ops, once req.Compares hold and the live count under Src equals
// req.ExpectedKeys, and returns that revision. An MVCC service that cannot
// move a prefix answers *service.PrefixMoveUnsupportedError.
func (t *Transport) MovePrefix(ctx context.Context, req *transport.MovePrefixRequest) (int64, error) {
	ctx, err := t.admit(ctx, "MovePrefix")
	if err != nil {
		return 0, err
	}
	if err := t.authorize(ctx, "MovePrefix"); err != nil {
		return 0, err
	}
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}
	if t.mvccSvc == nil {
		return 0, &NilServiceError{Service: "MVCCKVServicer"}
	}
	if req == nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: &transport.NilRequestError{}}
	}
	mover, ok := t.mvccSvc.(service.PrefixMover)
	if !ok {
		return 0, translateError(&service.PrefixMoveUnsupportedError{})
	}
	rev, err := mover.MovePrefix(ctx, &service.MovePrefixRequest{
		Src:          string(req.Src),
		Dst:          string(req.Dst),
		ExpectedKeys: req.ExpectedKeys,
		Ops:          toServiceOps(req.Ops),
		Compares:     toServiceCompares(req.Compares),
	})
	if err != nil {
		return 0, translateError(err)
	}
	return rev, nil
}

func handleMovePrefix(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.MovePrefixRequest)
	if !ok {
		return &InvalidRequestError{Method: "MovePrefix"}
	}
	rev, err := t.MovePrefix(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(*int64); ok {
		*out = rev
	}
	return nil
}

// MovePrefix resolves the caller's database's MVCC service and moves the
// prefix there. PrefixMover is optional on service.MVCCKVServicer, so it is
// not in the generated method set; a resolved service without it answers
// *service.PrefixMoveUnsupportedError.
func (s *scopedMVCC) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	svc, err := s.scope.registry.MVCCService(s.scope.tenant(ctx), s.scope.database)
	if err != nil {
		return 0, err
	}
	mover, ok := svc.(service.PrefixMover)
	if !ok {
		return 0, &service.PrefixMoveUnsupportedError{}
	}
	return mover.MovePrefix(ctx, req)
}

var _ service.PrefixMover = (*scopedMVCC)(nil)
