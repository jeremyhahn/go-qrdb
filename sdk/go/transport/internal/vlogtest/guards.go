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

// Package vlogtest is the VlogClient suite every SDK transport runs: Run
// drives a log through every method against a real server, and RunGuards
// holds the refusals a client makes before anything reaches the wire. One
// suite keeps the transports answering the same values.
package vlogtest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// RunGuards requires that open refuses every nil request with a
// RequestError naming the method around a NilRequestError, and that closed,
// a transport already closed, refuses every method with a ClosedError.
func RunGuards(t *testing.T, open, closed transport.VlogClient) {
	t.Helper()
	ctx := context.Background()
	for method, err := range calls(ctx, open, true) {
		var re *transport.RequestError
		var nre *transport.NilRequestError
		if !errors.As(err, &re) || re.Method != method || !errors.As(err, &nre) {
			t.Errorf("%s(nil): err = %v, want RequestError{%s, NilRequestError}", method, err, method)
		}
	}
	for method, err := range calls(ctx, closed, false) {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s on a closed transport: err = %v, want ClosedError", method, err)
		}
	}
}

// calls drives every method that takes a request, with a nil one when nilReq
// is set, and returns each call's error by method. VlogList is driven only
// with a request: its request has no fields, so nil means the same.
func calls(ctx context.Context, c transport.VlogClient, nilReq bool) map[string]error {
	out := map[string]error{}
	if nilReq {
		_, out["VlogCreate"] = c.VlogCreate(ctx, nil)
		_, out["VlogAppend"] = c.VlogAppend(ctx, nil)
		_, out["VlogGet"] = c.VlogGet(ctx, nil)
		_, out["VlogProveInclusion"] = c.VlogProveInclusion(ctx, nil)
		_, out["VlogProveConsistency"] = c.VlogProveConsistency(ctx, nil)
		_, out["VlogHead"] = c.VlogHead(ctx, nil)
		_, out["VlogSignHead"] = c.VlogSignHead(ctx, nil)
		_, out["VlogRotateKey"] = c.VlogRotateKey(ctx, nil)
		_, out["VlogExpireBodies"] = c.VlogExpireBodies(ctx, nil)
		return out
	}
	_, out["VlogCreate"] = c.VlogCreate(ctx, &vlogwire.CreateRequest{Log: "l"})
	_, out["VlogList"] = c.VlogList(ctx, &vlogwire.ListRequest{})
	_, out["VlogAppend"] = c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: "l"})
	_, out["VlogGet"] = c.VlogGet(ctx, &vlogwire.GetRequest{Log: "l"})
	_, out["VlogProveInclusion"] = c.VlogProveInclusion(ctx, &vlogwire.InclusionRequest{Log: "l"})
	_, out["VlogProveConsistency"] = c.VlogProveConsistency(ctx, &vlogwire.ConsistencyRequest{Log: "l"})
	_, out["VlogHead"] = c.VlogHead(ctx, &vlogwire.LogRequest{Log: "l"})
	_, out["VlogSignHead"] = c.VlogSignHead(ctx, &vlogwire.LogRequest{Log: "l"})
	_, out["VlogRotateKey"] = c.VlogRotateKey(ctx, &vlogwire.RotateKeyRequest{Log: "l"})
	_, out["VlogExpireBodies"] = c.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: "l"})
	return out
}
