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

package rest

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// Compile-time interface assertion.
var _ transport.VlogClient = (*Transport)(nil)

// vlogPost POSTs req as JSON to path and decodes the vlogwire result. isNil
// is the caller's nil check on its typed request, which an interface
// parameter cannot make.
func vlogPost[Res any](t *Transport, ctx context.Context, method, path string, req any, isNil bool) (*Res, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if isNil {
		return nil, vlogNilRequest(method)
	}
	var res Res
	if err := t.postJSONDecode(ctx, method, path, req, &res); err != nil {
		return nil, vlogNotFound(err)
	}
	return &res, nil
}

// vlogGet GETs path with query and decodes the vlogwire result.
func vlogGet[Res any](t *Transport, ctx context.Context, method, path string, query url.Values) (*Res, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var res Res
	if err := t.getJSONDecode(ctx, method, path, &res); err != nil {
		return nil, vlogNotFound(err)
	}
	return &res, nil
}

// vlogNotFound answers the server's 404 for a log that does not exist with
// the *transport.KeyNotFoundError every other wire transport returns for it,
// carrying the server's message as the gRPC mapping carries its status
// message.
func vlogNotFound(err error) error {
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound {
		return err
	}
	msg := reqErr.Method
	if reqErr.Err != nil {
		msg = reqErr.Err.Error()
	}
	return &transport.KeyNotFoundError{Key: msg}
}

// vlogNilRequest is the error for a nil request struct.
func vlogNilRequest(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}

// vlogUint formats a query parameter.
func vlogUint(v uint64) string {
	return strconv.FormatUint(v, 10)
}

// VlogCreate creates a log. Maps to POST /vlog/log.
func (t *Transport) VlogCreate(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	return vlogPost[vlogwire.CreateResult](t, ctx, "VlogCreate", "/vlog/log", req, req == nil)
}

// VlogList lists the database's logs. Maps to GET /vlog/logs.
func (t *Transport) VlogList(ctx context.Context, _ *vlogwire.ListRequest) (*vlogwire.ListResult, error) {
	return vlogGet[vlogwire.ListResult](t, ctx, "VlogList", "/vlog/logs", nil)
}

// VlogAppend appends entries. Maps to POST /vlog/append.
func (t *Transport) VlogAppend(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	return vlogPost[vlogwire.AppendResult](t, ctx, "VlogAppend", "/vlog/append", req, req == nil)
}

// VlogGet reads entries. Maps to GET /vlog/entries; a zero limit is left
// out, asking for the service's default.
func (t *Transport) VlogGet(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, vlogNilRequest("VlogGet")
	}
	q := url.Values{"log": {req.Log}, "from": {vlogUint(req.From)}}
	if req.Limit != 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	return vlogGet[vlogwire.GetResult](t, ctx, "VlogGet", "/vlog/entries", q)
}

// VlogProveInclusion returns an inclusion proof. Maps to
// GET /vlog/proof/inclusion.
func (t *Transport) VlogProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, vlogNilRequest("VlogProveInclusion")
	}
	q := url.Values{"log": {req.Log}, "index": {vlogUint(req.Index)}, "size": {vlogUint(req.Size)}}
	return vlogGet[vlogwire.InclusionProof](t, ctx, "VlogProveInclusion", "/vlog/proof/inclusion", q)
}

// VlogProveConsistency returns a consistency proof. Maps to
// GET /vlog/proof/consistency.
func (t *Transport) VlogProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, vlogNilRequest("VlogProveConsistency")
	}
	q := url.Values{"log": {req.Log}, "size1": {vlogUint(req.Size1)}, "size2": {vlogUint(req.Size2)}}
	return vlogGet[vlogwire.ConsistencyProof](t, ctx, "VlogProveConsistency", "/vlog/proof/consistency", q)
}

// VlogHead returns a log's head. Maps to GET /vlog/head.
func (t *Transport) VlogHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, vlogNilRequest("VlogHead")
	}
	return vlogGet[vlogwire.Head](t, ctx, "VlogHead", "/vlog/head", url.Values{"log": {req.Log}})
}

// VlogSignHead signs a log's head. Maps to POST /vlog/head/sign.
func (t *Transport) VlogSignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	return vlogPost[vlogwire.SignHeadResult](t, ctx, "VlogSignHead", "/vlog/head/sign", req, req == nil)
}

// VlogRotateKey rotates a log's head-signing key. Maps to
// POST /vlog/key/rotate.
func (t *Transport) VlogRotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	return vlogPost[vlogwire.RotateKeyResult](t, ctx, "VlogRotateKey", "/vlog/key/rotate", req, req == nil)
}

// VlogExpireBodies expires entry bodies. Maps to POST /vlog/expire.
func (t *Transport) VlogExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	return vlogPost[vlogwire.ExpireResult](t, ctx, "VlogExpireBodies", "/vlog/expire", req, req == nil)
}
