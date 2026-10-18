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

// namespacedBatchService wraps a BatchServicer so that the keys of every
// operation are transparently prefixed with the composite (tenant, db)
// namespace and stripped from any key-values echoed in the responses. Entity
// operations have their entity ID prefixed, mirroring the namespaced KV entity
// methods so that the resulting entity keys match.
type namespacedBatchService struct {
	dbGuard
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
// confined to the namespace with prefixKeyBytes (an absent key maps to the
// bare namespace prefix rather than escaping to the shared store root — see
// prefixKeyBytes) and the (nil) RangeEnd is preserved with prefixBytes so it
// is not misread as a range.
func (s *namespacedBatchService) prefixOps(ops []service.Op) []service.Op {
	out := make([]service.Op, len(ops))
	for i, op := range ops {
		if isRangeOp(op) {
			op.Key = prefixStartBytes(op.Key, s.prefix)
			op.RangeEnd = prefixEndBytes(op.RangeEnd, s.prefix)
		} else {
			op.Key = prefixKeyBytes(op.Key, s.prefix)
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
func (s *namespacedBatchService) Execute(ctx context.Context, ops []service.Op) (*service.BatchResponse, error) {
	ctx = s.scope(ctx)
	result, err := s.inner.Execute(ctx, s.prefixOps(ops))
	if err != nil {
		return nil, err
	}
	return &service.BatchResponse{Revision: result.Revision, Responses: s.stripResponses(result.Responses)}, nil
}

// ExecuteWithMode applies the operations atomically with the specified write
// durability under the namespace prefix.
func (s *namespacedBatchService) ExecuteWithMode(ctx context.Context, ops []service.Op, mode writemode.Mode) (*service.BatchResponse, error) {
	ctx = s.scope(ctx)
	result, err := s.inner.ExecuteWithMode(ctx, s.prefixOps(ops), mode)
	if err != nil {
		return nil, err
	}
	return &service.BatchResponse{Revision: result.Revision, Responses: s.stripResponses(result.Responses)}, nil
}

// ExecuteEntity executes entity-typed operations with their entity IDs prefixed
// so the resulting entity keys are scoped to the namespace.
func (s *namespacedBatchService) ExecuteEntity(ctx context.Context, req *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	ctx = s.scope(ctx)
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

// ImportBegin scopes a chunked import session to the composite namespace
// by setting KeyPrefix on opts, so imported keys land in this database's
// keyspace exactly like every other write; the inner service prefixes each
// stored key and still reports the original user-supplied key in any error
// rows. The inner service also folds KeyPrefix into the session ID it
// returns, so two sessions opened against two different namespaces
// sharing one underlying shard (prefix isolation) can never resolve to the
// same hidden key even if their bare IDs collided.
func (s *namespacedBatchService) ImportBegin(ctx context.Context, opts service.ImportOptions) (string, error) {
	ctx = s.scope(ctx)
	opts.KeyPrefix = s.prefix + opts.KeyPrefix
	return s.inner.ImportBegin(ctx, opts)
}

// checkSessionNamespace refuses a session ID ImportBegin did not mint
// through THIS wrapper's own (tenant, database) namespace: the inner
// service's checkSessionOwner checks only the caller's (tenant, identity)
// against what ImportBegin recorded, never the database, so without this a
// caller could ImportAppend/ImportCommit/ImportAbort a session begun through
// a DIFFERENT namespaced database wrapper of the same tenant it also has
// access to (go-qrdb#576's security review). The refusal is the inner
// service's own *service.ImportSessionNotFoundError -- indistinguishable
// from "no such session" -- rather than a distinct error that would confirm
// the session exists in some other database.
func (s *namespacedBatchService) checkSessionNamespace(sessionID string) error {
	if !service.ImportSessionHasPrefix(sessionID, s.prefix) {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	return nil
}

// ImportAppend forwards to the inner service once checkSessionNamespace
// confirms sessionID belongs to this wrapper's own namespace.
func (s *namespacedBatchService) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	ctx = s.scope(ctx)
	if err := s.checkSessionNamespace(sessionID); err != nil {
		return err
	}
	return s.inner.ImportAppend(ctx, sessionID, seq, chunk)
}

// ImportCommit forwards to the inner service. See ImportAppend.
func (s *namespacedBatchService) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	ctx = s.scope(ctx)
	if err := s.checkSessionNamespace(sessionID); err != nil {
		return nil, err
	}
	return s.inner.ImportCommit(ctx, sessionID)
}

// ImportAbort forwards to the inner service. See ImportAppend.
func (s *namespacedBatchService) ImportAbort(ctx context.Context, sessionID string) error {
	ctx = s.scope(ctx)
	if err := s.checkSessionNamespace(sessionID); err != nil {
		return err
	}
	return s.inner.ImportAbort(ctx, sessionID)
}
