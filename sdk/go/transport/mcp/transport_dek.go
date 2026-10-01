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
	"encoding/base64"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// DEKClient implementation over the qrdb_dek_* tools served by
// pkg/api/mcp/tools_dek.go. Byte arguments are base64-encoded; each tool
// answers with the same JSON body the REST transport returns.

// TenantDEKStatus calls qrdb_dek_status.
func (t *Transport) TenantDEKStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKStatus", Err: &transport.NilRequestError{}}
	}
	var resp transport.TenantDEKStatusResponse
	if err := t.dekTool(ctx, "TenantDEKStatus", "qrdb_dek_status", dekArgs(req.TenantID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKRotate calls qrdb_dek_rotate.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKRotate", Err: &transport.NilRequestError{}}
	}
	var resp transport.TenantDEKStatusResponse
	if err := t.dekTool(ctx, "TenantDEKRotate", "qrdb_dek_rotate", dekArgs(req.TenantID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKRetire calls qrdb_dek_retire.
func (t *Transport) TenantDEKRetire(ctx context.Context, req *transport.TenantDEKRetireRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKRetire", Err: &transport.NilRequestError{}}
	}
	args := dekArgs(req.TenantID)
	args["version"] = req.Version
	var resp transport.TenantDEKStatusResponse
	if err := t.dekTool(ctx, "TenantDEKRetire", "qrdb_dek_retire", args, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKEncrypt calls qrdb_dek_encrypt.
func (t *Transport) TenantDEKEncrypt(ctx context.Context, req *transport.TenantDEKEncryptRequest) (*transport.TenantDEKEncryptResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKEncrypt", Err: &transport.NilRequestError{}}
	}
	var resp transport.TenantDEKEncryptResponse
	if err := t.dekTool(ctx, "TenantDEKEncrypt", "qrdb_dek_encrypt", dekDataArgs(req.TenantID, "plaintext", req.Plaintext, req.AAD), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKDecrypt calls qrdb_dek_decrypt.
func (t *Transport) TenantDEKDecrypt(ctx context.Context, req *transport.TenantDEKDecryptRequest) (*transport.TenantDEKDecryptResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKDecrypt", Err: &transport.NilRequestError{}}
	}
	var resp transport.TenantDEKDecryptResponse
	if err := t.dekTool(ctx, "TenantDEKDecrypt", "qrdb_dek_decrypt", dekDataArgs(req.TenantID, "ciphertext", req.Ciphertext, req.AAD), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantDEKRewrap calls qrdb_dek_rewrap.
func (t *Transport) TenantDEKRewrap(ctx context.Context, req *transport.TenantDEKRewrapRequest) (*transport.TenantDEKRewrapResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantDEKRewrap", Err: &transport.NilRequestError{}}
	}
	var resp transport.TenantDEKRewrapResponse
	if err := t.dekTool(ctx, "TenantDEKRewrap", "qrdb_dek_rewrap", dekDataArgs(req.TenantID, "ciphertext", req.Ciphertext, req.AAD), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// dekTool calls a keyring tool and decodes its JSON result into dest.
func (t *Transport) dekTool(ctx context.Context, method, tool string, args map[string]any, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	if err := json.Unmarshal([]byte(text), dest); err != nil {
		return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
	}
	return nil
}

// dekArgs returns the tool arguments carrying the optional tenant.
func dekArgs(tenantID string) map[string]any {
	args := map[string]any{}
	if tenantID != "" {
		args["tenant_id"] = tenantID
	}
	return args
}

// dekDataArgs returns the tool arguments for a data-carrying keyring tool.
func dekDataArgs(tenantID, field string, data, aad []byte) map[string]any {
	args := dekArgs(tenantID)
	args[field] = base64.StdEncoding.EncodeToString(data)
	if len(aad) > 0 {
		args["aad"] = base64.StdEncoding.EncodeToString(aad)
	}
	return args
}
