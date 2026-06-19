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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// namespacedTransactionService wraps a TransactionServicer so that the keys of
// every compare and operation are transparently prefixed with the composite
// (tenant, db) namespace and stripped from any key-values echoed in the
// response. Entity-typed transactions have their entity ID prefixed, mirroring
// the namespaced KV entity methods so the resulting entity keys match.
type namespacedTransactionService struct {
	inner  service.TransactionServicer
	prefix string
}

// Compile-time interface check.
var _ service.TransactionServicer = (*namespacedTransactionService)(nil)

// newNamespacedTransactionService creates a TransactionServicer scoped to the
// given composite namespace prefix.
func newNamespacedTransactionService(inner service.TransactionServicer, prefix string) *namespacedTransactionService {
	return &namespacedTransactionService{inner: inner, prefix: prefix}
}

// prefixCompares returns a copy of compares with each key namespaced. A compare
// targets a single key, so an absent (nil/empty) key is confined to the
// namespace prefix rather than the shared-store root, preventing a compare from
// observing another tenant's keyspace.
func (s *namespacedTransactionService) prefixCompares(compares []service.Compare) []service.Compare {
	out := make([]service.Compare, len(compares))
	for i, c := range compares {
		c.Key = prefixStartBytes(c.Key, s.prefix)
		out[i] = c
	}
	return out
}

// prefixOps returns a copy of ops with each key and range end namespaced. Range
// ops have both bounds confined to the namespace (an absent bound stays within
// it); single-key ops have their Key prefixed and a nil RangeEnd preserved. See
// the batch service prefixOps for the rationale.
func (s *namespacedTransactionService) prefixOps(ops []service.Op) []service.Op {
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

// stripResponse returns a copy of resp with key-value keys stripped from each
// operation response.
func (s *namespacedTransactionService) stripResponse(resp *service.TransactionResponse) *service.TransactionResponse {
	if resp == nil {
		return nil
	}
	out := *resp
	if resp.Responses != nil {
		responses := make([]service.OpResponse, len(resp.Responses))
		for i, r := range resp.Responses {
			r.Kvs = stripKeyValues(r.Kvs, s.prefix)
			responses[i] = r
		}
		out.Responses = responses
	}
	return &out
}

// Execute evaluates the prefixed compares and applies the prefixed operations
// atomically.
func (s *namespacedTransactionService) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	resp, err := s.inner.Execute(ctx, s.prefixCompares(compares), s.prefixOps(successOps), s.prefixOps(failureOps))
	if err != nil {
		return nil, err
	}
	return s.stripResponse(resp), nil
}

// ExecuteWithMode evaluates the prefixed compares and applies the prefixed
// operations atomically with the specified write durability.
func (s *namespacedTransactionService) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
	resp, err := s.inner.ExecuteWithMode(ctx, s.prefixCompares(compares), s.prefixOps(successOps), s.prefixOps(failureOps), mode)
	if err != nil {
		return nil, err
	}
	return s.stripResponse(resp), nil
}

// ExecuteEntity evaluates entity-typed compares and applies entity-typed
// operations with their entity IDs prefixed so the resulting keys are scoped to
// the namespace. The response carries raw operation payloads (no keys), so it
// is forwarded unchanged.
func (s *namespacedTransactionService) ExecuteEntity(ctx context.Context, req *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	if req == nil {
		return s.inner.ExecuteEntity(ctx, req)
	}
	cp := *req
	cp.Compares = make([]service.EntityCompare, len(req.Compares))
	for i, c := range req.Compares {
		c.ID = s.prefix + c.ID
		cp.Compares[i] = c
	}
	cp.Success = s.prefixEntityOps(req.Success)
	cp.Failure = s.prefixEntityOps(req.Failure)
	return s.inner.ExecuteEntity(ctx, &cp)
}

// prefixEntityOps returns a copy of ops with each entity ID prefixed.
func (s *namespacedTransactionService) prefixEntityOps(ops []service.TransactionEntityOp) []service.TransactionEntityOp {
	out := make([]service.TransactionEntityOp, len(ops))
	for i, op := range ops {
		op.ID = s.prefix + op.ID
		out[i] = op
	}
	return out
}
