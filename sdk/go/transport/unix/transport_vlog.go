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

package unix

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// Compile-time interface assertion.
var _ transport.VlogClient = (*Transport)(nil)

// VlogClient implementation: the QRDBService Vlog* RPCs over Unix socket, issued
// through the vlogconv.Caller the gRPC and Unix transports share. The server
// handlers live in pkg/api/grpc/service_vlog.go.

// vlog returns the shared RPC caller over this transport's client.
func (t *Transport) vlog() vlogconv.Caller {
	return vlogconv.Caller{Client: t.client, MapError: mapGRPCError}
}

// VlogCreate creates a log.
func (t *Transport) VlogCreate(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().Create(ctx, req)
}

// VlogList lists the database's logs.
func (t *Transport) VlogList(ctx context.Context, req *vlogwire.ListRequest) (*vlogwire.ListResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().List(ctx, req)
}

// VlogAppend appends entries to a log.
func (t *Transport) VlogAppend(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().Append(ctx, req)
}

// VlogGet reads a run of a log's entries.
func (t *Transport) VlogGet(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().Get(ctx, req)
}

// VlogProveInclusion returns an inclusion proof.
func (t *Transport) VlogProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().ProveInclusion(ctx, req)
}

// VlogProveConsistency returns a consistency proof.
func (t *Transport) VlogProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().ProveConsistency(ctx, req)
}

// VlogHead returns a log's head.
func (t *Transport) VlogHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().Head(ctx, req)
}

// VlogSignHead signs a log's head.
func (t *Transport) VlogSignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().SignHead(ctx, req)
}

// VlogRotateKey rotates a log's head-signing key.
func (t *Transport) VlogRotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().RotateKey(ctx, req)
}

// VlogExpireBodies expires entry bodies before a cutoff.
func (t *Transport) VlogExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.vlog().ExpireBodies(ctx, req)
}
