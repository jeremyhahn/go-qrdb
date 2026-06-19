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

package server

import (
	"context"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// namespacedBatchService wraps a BatchServicer so that the keys of every
// operation are transparently prefixed with the composite (tenant, db)
// namespace and stripped from any key-values echoed in the responses. Entity
// operations have their entity ID prefixed, mirroring the namespaced KV entity
// methods so that the resulting entity keys match.
type namespacedBatchService struct {
	inner  service.BatchServicer
	prefix string
}

// Compile-time interface check.
var _ service.BatchServicer = (*namespacedBatchService)(nil)

// newNamespacedBatchService creates a BatchServicer scoped to the given
// composite namespace prefix.
func newNamespacedBatchService(inner service.BatchServicer, prefix string) *namespacedBatchService {
	return &namespacedBatchService{inner: inner, prefix: prefix}
}

// prefixOps returns a copy of ops with each key and range end namespaced. For a
// range op (an explicit OpRange, or any op that carries a RangeEnd) the Key is
// the inclusive lower bound and RangeEnd the exclusive upper bound, so both are
// confined to the namespace — an absent bound stays within the namespace
// instead of escaping to the shared store. For a single-key op the Key is
// prefixed and the (nil) RangeEnd is preserved so it is not misread as a range.
func (s *namespacedBatchService) prefixOps(ops []service.Op) []service.Op {
	out := make([]service.Op, len(ops))
	for i, op := range ops {
		if isRangeOp(op) {
			op.Key = prefixStartBytes(op.Key, s.prefix)
			op.RangeEnd = prefixEndBytes(op.RangeEnd, s.prefix)
		} else {
			op.Key = prefixBytes(op.Key, s.prefix)
			op.RangeEnd = prefixBytes(op.RangeEnd, s.prefix)
		}
		out[i] = op
	}
	return out
}

// stripResponses returns a copy of responses with key-value keys stripped.
func (s *namespacedBatchService) stripResponses(responses []service.OpResponse) []service.OpResponse {
	out := make([]service.OpResponse, len(responses))
	for i, resp := range responses {
		resp.Kvs = stripKeyValues(resp.Kvs, s.prefix)
		out[i] = resp
	}
	return out
}

// Execute applies the operations atomically under the namespace prefix.
func (s *namespacedBatchService) Execute(ctx context.Context, ops []service.Op) ([]service.OpResponse, error) {
	responses, err := s.inner.Execute(ctx, s.prefixOps(ops))
	if err != nil {
		return nil, err
	}
	return s.stripResponses(responses), nil
}

// ExecuteWithMode applies the operations atomically with the specified write
// durability under the namespace prefix.
func (s *namespacedBatchService) ExecuteWithMode(ctx context.Context, ops []service.Op, mode writemode.Mode) ([]service.OpResponse, error) {
	responses, err := s.inner.ExecuteWithMode(ctx, s.prefixOps(ops), mode)
	if err != nil {
		return nil, err
	}
	return s.stripResponses(responses), nil
}

// ExecuteEntity executes entity-typed operations with their entity IDs prefixed
// so the resulting entity keys are scoped to the namespace.
func (s *namespacedBatchService) ExecuteEntity(ctx context.Context, req *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	if req == nil {
		return s.inner.ExecuteEntity(ctx, req)
	}
	cp := *req
	cp.Ops = make([]service.BatchEntityOp, len(req.Ops))
	for i, op := range req.Ops {
		op.ID = s.prefix + op.ID
		cp.Ops[i] = op
	}
	return s.inner.ExecuteEntity(ctx, &cp)
}

// Import is not supported for a namespaced database scope because ImportOptions
// carries no key-prefix hook. See NamespacedImportUnsupportedError.
func (s *namespacedBatchService) Import(ctx context.Context, file io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	return nil, &NamespacedImportUnsupportedError{Prefix: s.prefix}
}
