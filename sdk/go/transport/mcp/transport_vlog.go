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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// Compile-time interface assertion.
var _ transport.VlogClient = (*Transport)(nil)

// vlogTool calls a qrdb_vlog_* tool with req's vlogwire JSON as its
// arguments and decodes the vlogwire result. The tools are data-plane, so
// callTool carries the transport's database. isNil is the caller's nil check
// on its typed request, which an interface parameter cannot make.
func vlogTool[Res any](t *Transport, ctx context.Context, method, tool string, req any, isNil bool) (*Res, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if isNil {
		return nil, &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
	}
	args, err := wire.EncodeArgs(req)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	var res Res
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return nil, &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
	}
	return &res, nil
}

// VlogCreate creates a log. Calls the qrdb_vlog_create tool.
func (t *Transport) VlogCreate(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	return vlogTool[vlogwire.CreateResult](t, ctx, "VlogCreate", "qrdb_vlog_create", req, req == nil)
}

// VlogList lists the database's logs. Calls the qrdb_vlog_list tool.
func (t *Transport) VlogList(ctx context.Context, req *vlogwire.ListRequest) (*vlogwire.ListResult, error) {
	if req == nil {
		req = &vlogwire.ListRequest{}
	}
	return vlogTool[vlogwire.ListResult](t, ctx, "VlogList", "qrdb_vlog_list", req, false)
}

// VlogAppend appends entries to a log. Calls the qrdb_vlog_append tool.
func (t *Transport) VlogAppend(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	return vlogTool[vlogwire.AppendResult](t, ctx, "VlogAppend", "qrdb_vlog_append", req, req == nil)
}

// VlogGet reads a run of a log's entries. Calls the qrdb_vlog_get tool.
func (t *Transport) VlogGet(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	return vlogTool[vlogwire.GetResult](t, ctx, "VlogGet", "qrdb_vlog_get", req, req == nil)
}

// VlogProveInclusion returns an inclusion proof. Calls the qrdb_vlog_prove_inclusion tool.
func (t *Transport) VlogProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	return vlogTool[vlogwire.InclusionProof](t, ctx, "VlogProveInclusion", "qrdb_vlog_prove_inclusion", req, req == nil)
}

// VlogProveConsistency returns a consistency proof. Calls the qrdb_vlog_prove_consistency tool.
func (t *Transport) VlogProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	return vlogTool[vlogwire.ConsistencyProof](t, ctx, "VlogProveConsistency", "qrdb_vlog_prove_consistency", req, req == nil)
}

// VlogHead returns a log's head. Calls the qrdb_vlog_head tool.
func (t *Transport) VlogHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	return vlogTool[vlogwire.Head](t, ctx, "VlogHead", "qrdb_vlog_head", req, req == nil)
}

// VlogSignHead signs a log's head. Calls the qrdb_vlog_sign_head tool.
func (t *Transport) VlogSignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	return vlogTool[vlogwire.SignHeadResult](t, ctx, "VlogSignHead", "qrdb_vlog_sign_head", req, req == nil)
}

// VlogRotateKey rotates a log's head-signing key. Calls the qrdb_vlog_rotate_key tool.
func (t *Transport) VlogRotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	return vlogTool[vlogwire.RotateKeyResult](t, ctx, "VlogRotateKey", "qrdb_vlog_rotate_key", req, req == nil)
}

// VlogExpireBodies expires entry bodies before a cutoff. Calls the qrdb_vlog_expire_bodies tool.
func (t *Transport) VlogExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	return vlogTool[vlogwire.ExpireResult](t, ctx, "VlogExpireBodies", "qrdb_vlog_expire_bodies", req, req == nil)
}
