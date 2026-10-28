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
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// Compile-time interface assertion.
var _ transport.VlogClient = (*Transport)(nil)

// QUIC vlog method names; the payloads are the sdk/go/transport/vlogwire JSON.
const (
	methodVlogCreate           = "vlog.Create"
	methodVlogList             = "vlog.List"
	methodVlogAppend           = "vlog.Append"
	methodVlogGet              = "vlog.Get"
	methodVlogProveInclusion   = "vlog.ProveInclusion"
	methodVlogProveConsistency = "vlog.ProveConsistency"
	methodVlogHead             = "vlog.Head"
	methodVlogSignHead         = "vlog.SignHead"
	methodVlogRotateKey        = "vlog.RotateKey"
	methodVlogExpireBodies     = "vlog.ExpireBodies"
)

// vlogCall sends req as JSON to the QUIC method and decodes the vlogwire
// result. name is the SDK method reported in a RequestError; isNil is the
// caller's nil check on its typed request, which an interface parameter
// cannot make.
func vlogCall[Res any](t *Transport, ctx context.Context, name, method string, req any, isNil bool) (*Res, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if isNil {
		return nil, dekNilRequest(name)
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: name, Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, method, payload)
	if err != nil {
		return nil, &transport.RequestError{Method: name, Err: err}
	}

	var res Res
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, &transport.RequestError{Method: name, Err: &UnmarshalError{Err: err}}
	}
	return &res, nil
}

// VlogCreate creates a log via the QUIC vlog.Create method.
func (t *Transport) VlogCreate(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	return vlogCall[vlogwire.CreateResult](t, ctx, "VlogCreate", methodVlogCreate, req, req == nil)
}

// VlogList lists the database's logs via the QUIC vlog.List method.
func (t *Transport) VlogList(ctx context.Context, req *vlogwire.ListRequest) (*vlogwire.ListResult, error) {
	if req == nil {
		req = &vlogwire.ListRequest{}
	}
	return vlogCall[vlogwire.ListResult](t, ctx, "VlogList", methodVlogList, req, false)
}

// VlogAppend appends entries via the QUIC vlog.Append method.
func (t *Transport) VlogAppend(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	return vlogCall[vlogwire.AppendResult](t, ctx, "VlogAppend", methodVlogAppend, req, req == nil)
}

// VlogGet reads entries via the QUIC vlog.Get method.
func (t *Transport) VlogGet(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	return vlogCall[vlogwire.GetResult](t, ctx, "VlogGet", methodVlogGet, req, req == nil)
}

// VlogProveInclusion returns an inclusion proof via the QUIC
// vlog.ProveInclusion method.
func (t *Transport) VlogProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	return vlogCall[vlogwire.InclusionProof](t, ctx, "VlogProveInclusion", methodVlogProveInclusion, req, req == nil)
}

// VlogProveConsistency returns a consistency proof via the QUIC
// vlog.ProveConsistency method.
func (t *Transport) VlogProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	return vlogCall[vlogwire.ConsistencyProof](t, ctx, "VlogProveConsistency", methodVlogProveConsistency, req, req == nil)
}

// VlogHead returns a log's head via the QUIC vlog.Head method.
func (t *Transport) VlogHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	return vlogCall[vlogwire.Head](t, ctx, "VlogHead", methodVlogHead, req, req == nil)
}

// VlogSignHead signs a log's head via the QUIC vlog.SignHead method.
func (t *Transport) VlogSignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	return vlogCall[vlogwire.SignHeadResult](t, ctx, "VlogSignHead", methodVlogSignHead, req, req == nil)
}

// VlogRotateKey rotates a log's head-signing key via the QUIC vlog.RotateKey
// method.
func (t *Transport) VlogRotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	return vlogCall[vlogwire.RotateKeyResult](t, ctx, "VlogRotateKey", methodVlogRotateKey, req, req == nil)
}

// VlogExpireBodies expires entry bodies via the QUIC vlog.ExpireBodies
// method.
func (t *Transport) VlogExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	return vlogCall[vlogwire.ExpireResult](t, ctx, "VlogExpireBodies", methodVlogExpireBodies, req, req == nil)
}
