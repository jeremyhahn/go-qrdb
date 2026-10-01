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

	"github.com/jeremyhahn/go-qrdb/pkg/api/dekwire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// DEKClient implementation: direct calls into the injected
// service.DEKServicer. The tenant acted on is the one bound to ctx (see
// sdk.ContextWithTenant), or the system tenant when none is bound; a request
// naming any other tenant is refused by the service.

// TenantDEKStatus returns the caller's keyring versions.
func (t *Transport) TenantDEKStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if err := t.dekReady("TenantDEKStatus", req == nil); err != nil {
		return nil, err
	}
	st, err := t.dekSvc.Status(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	return dekwire.Status(st), nil
}

// TenantDEKRotate adds a key version and makes it current.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if err := t.dekReady("TenantDEKRotate", req == nil); err != nil {
		return nil, err
	}
	st, err := t.dekSvc.Rotate(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	return dekwire.Status(st), nil
}

// TenantDEKRetire destroys an old key version.
func (t *Transport) TenantDEKRetire(ctx context.Context, req *transport.TenantDEKRetireRequest) (*transport.TenantDEKStatusResponse, error) {
	if err := t.dekReady("TenantDEKRetire", req == nil); err != nil {
		return nil, err
	}
	st, err := t.dekSvc.Retire(ctx, req.TenantID, req.Version)
	if err != nil {
		return nil, err
	}
	return dekwire.Status(st), nil
}

// TenantDEKEncrypt seals plaintext under the current key version.
func (t *Transport) TenantDEKEncrypt(ctx context.Context, req *transport.TenantDEKEncryptRequest) (*transport.TenantDEKEncryptResponse, error) {
	if err := t.dekReady("TenantDEKEncrypt", req == nil); err != nil {
		return nil, err
	}
	sealed, err := t.dekSvc.Encrypt(ctx, req.TenantID, req.Plaintext, req.AAD)
	if err != nil {
		return nil, err
	}
	return dekwire.Encrypt(sealed), nil
}

// TenantDEKDecrypt opens a ciphertext sealed for the caller's tenant.
func (t *Transport) TenantDEKDecrypt(ctx context.Context, req *transport.TenantDEKDecryptRequest) (*transport.TenantDEKDecryptResponse, error) {
	if err := t.dekReady("TenantDEKDecrypt", req == nil); err != nil {
		return nil, err
	}
	opened, err := t.dekSvc.Decrypt(ctx, req.TenantID, req.Ciphertext, req.AAD)
	if err != nil {
		return nil, err
	}
	return dekwire.Decrypt(opened), nil
}

// TenantDEKRewrap re-seals a ciphertext under the current key version.
func (t *Transport) TenantDEKRewrap(ctx context.Context, req *transport.TenantDEKRewrapRequest) (*transport.TenantDEKRewrapResponse, error) {
	if err := t.dekReady("TenantDEKRewrap", req == nil); err != nil {
		return nil, err
	}
	rw, err := t.dekSvc.Rewrap(ctx, req.TenantID, req.Ciphertext, req.AAD)
	if err != nil {
		return nil, err
	}
	return dekwire.Rewrap(rw), nil
}

// dekReady checks the transport is open, the service is wired and the
// request is non-nil.
func (t *Transport) dekReady(method string, nilRequest bool) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return &NilServiceError{Service: "DEKServicer"}
	}
	if nilRequest {
		return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
	}
	return nil
}

// dekRequestHandler adapts a typed keyring method to the generic Request
// dispatch.
func dekRequestHandler[Req, Resp any](method string, call func(*Transport, context.Context, *Req) (*Resp, error)) requestHandler {
	return func(ctx context.Context, t *Transport, req, resp any) error {
		r, ok := req.(*Req)
		if !ok {
			return &InvalidRequestError{Method: method}
		}
		result, err := call(t, ctx, r)
		if err != nil {
			return err
		}
		if out, ok := resp.(**Resp); ok {
			*out = result
		}
		return nil
	}
}

// WithDEKService injects the tenant DEK keyring service into the transport.
func WithDEKService(svc service.DEKServicer) Option {
	return func(t *Transport) {
		t.dekSvc = svc
	}
}
