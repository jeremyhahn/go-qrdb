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
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// DEKClient implementation over the /v1/dek routes served by
// pkg/api/rest/handlers_dek.go. Byte fields travel base64-encoded in JSON.

// TenantDEKStatus maps to GET /v1/dek/status.
func (t *Transport) TenantDEKStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKStatus", Err: &transport.NilRequestError{}}
	}
	path := "/v1/dek/status"
	if req.TenantID != "" {
		path += "?tenant_id=" + url.QueryEscape(req.TenantID)
	}
	var resp transport.TenantDEKStatusResponse
	if err := t.getJSONDecode(ctx, "TenantDEKStatus", path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKRotate maps to POST /v1/dek/rotate.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	var resp transport.TenantDEKStatusResponse
	if err := t.dekPost(ctx, "TenantDEKRotate", "/v1/dek/rotate", req, req == nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKRetire maps to POST /v1/dek/retire.
func (t *Transport) TenantDEKRetire(ctx context.Context, req *transport.TenantDEKRetireRequest) (*transport.TenantDEKStatusResponse, error) {
	var resp transport.TenantDEKStatusResponse
	if err := t.dekPost(ctx, "TenantDEKRetire", "/v1/dek/retire", req, req == nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKEncrypt maps to POST /v1/dek/encrypt.
func (t *Transport) TenantDEKEncrypt(ctx context.Context, req *transport.TenantDEKEncryptRequest) (*transport.TenantDEKEncryptResponse, error) {
	var resp transport.TenantDEKEncryptResponse
	if err := t.dekPost(ctx, "TenantDEKEncrypt", "/v1/dek/encrypt", req, req == nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKDecrypt maps to POST /v1/dek/decrypt.
func (t *Transport) TenantDEKDecrypt(ctx context.Context, req *transport.TenantDEKDecryptRequest) (*transport.TenantDEKDecryptResponse, error) {
	var resp transport.TenantDEKDecryptResponse
	if err := t.dekPost(ctx, "TenantDEKDecrypt", "/v1/dek/decrypt", req, req == nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKRewrap maps to POST /v1/dek/rewrap.
func (t *Transport) TenantDEKRewrap(ctx context.Context, req *transport.TenantDEKRewrapRequest) (*transport.TenantDEKRewrapResponse, error) {
	var resp transport.TenantDEKRewrapResponse
	if err := t.dekPost(ctx, "TenantDEKRewrap", "/v1/dek/rewrap", req, req == nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// dekPost posts a keyring request. isNil is the caller's nil check on its
// typed request, which an interface parameter cannot make.
func (t *Transport) dekPost(ctx context.Context, method, path string, req any, isNil bool, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if isNil {
		return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
	}
	return t.postJSONDecode(ctx, method, path, req, dest)
}
