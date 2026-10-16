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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CredentialClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CredentialClient implementation
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value by calling the
// qrdb_credential_submit MCP tool.
func (t *Transport) CredentialSubmit(ctx context.Context, req *transport.CredentialSubmitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "CredentialSubmit", Err: &nilRequestError{Method: "CredentialSubmit"}}
	}

	// MCP tool arguments are strings; base64-encode (RFC 4648) so arbitrary
	// binary credential content round-trips byte-for-byte.
	_, err := t.callTool(ctx, "qrdb_credential_submit", map[string]any{
		"name":  req.Name,
		"value": base64.StdEncoding.EncodeToString(req.Value),
	})
	if err != nil {
		return &transport.RequestError{Method: "CredentialSubmit", Err: err}
	}
	return nil
}

// CredentialStrategy returns the active credential management strategy by
// calling the qrdb_credential_strategy MCP tool and decoding the JSON
// response.
func (t *Transport) CredentialStrategy(ctx context.Context) (*transport.CredentialStrategyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_credential_strategy", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialStrategy", Err: err}
	}

	var resp transport.CredentialStrategyResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CredentialStrategy", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}

// CredentialDelete removes a named credential and all of its non-current
// historical versions by calling the qrdb_credential_delete MCP tool.
func (t *Transport) CredentialDelete(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_credential_delete", map[string]any{
		"name": name,
	})
	if err != nil {
		return &transport.RequestError{Method: "CredentialDelete", Err: err}
	}
	return nil
}

// CredentialList returns the names of every stored credential by calling
// the qrdb_credential_list MCP tool. Values are never returned by
// enumeration.
func (t *Transport) CredentialList(ctx context.Context, req *transport.CredentialListRequest) (*transport.CredentialListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// req is reserved for future filter fields. A nil request is permitted.
	_ = req

	text, err := t.callTool(ctx, "qrdb_credential_list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialList", Err: err}
	}

	var resp transport.CredentialListResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CredentialList", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}

// CredentialRotate atomically replaces the current value of a credential,
// optionally keeping the previous version retrievable for a grace period,
// by calling the qrdb_credential_rotate MCP tool.
func (t *Transport) CredentialRotate(ctx context.Context, req *transport.CredentialRotateRequest) (*transport.CredentialRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: &nilRequestError{Method: "CredentialRotate"}}
	}

	// new_value is base64-encoded (RFC 4648) so binary credential content
	// round-trips byte-for-byte over the string-only MCP tool argument.
	text, err := t.callTool(ctx, "qrdb_credential_rotate", map[string]any{
		"name":                 req.Name,
		"new_value":            base64.StdEncoding.EncodeToString(req.NewValue),
		"grace_period_seconds": req.GracePeriodSeconds,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: err}
	}

	var resp transport.CredentialRotateResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}

// CredentialListVersions returns the version history (current + non-expired
// grace) for a credential by calling the qrdb_credential_list_versions MCP
// tool. Values are never returned by this call.
func (t *Transport) CredentialListVersions(ctx context.Context, req *transport.CredentialListVersionsRequest) (*transport.CredentialListVersionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: &nilRequestError{Method: "CredentialListVersions"}}
	}

	text, err := t.callTool(ctx, "qrdb_credential_list_versions", map[string]any{
		"name": req.Name,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: err}
	}

	var resp transport.CredentialListVersionsResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}

// CredentialGCExpired reaps any historical versions of a credential whose
// grace period has elapsed, returning the count reaped. Calls the
// qrdb_credential_gc_expired MCP tool.
func (t *Transport) CredentialGCExpired(ctx context.Context, req *transport.CredentialGCRequest) (*transport.CredentialGCResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: &nilRequestError{Method: "CredentialGCExpired"}}
	}

	text, err := t.callTool(ctx, "qrdb_credential_gc_expired", map[string]any{
		"name": req.Name,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: err}
	}

	var resp transport.CredentialGCResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &resp, nil
}

// CredentialStore persists a credential value via the qrdb_credential_store
// tool. The value is base64-encoded (RFC 4648) so arbitrary binary content
// round-trips byte-for-byte over the string-only MCP tool argument.
func (t *Transport) CredentialStore(ctx context.Context, name string, value []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.callTool(ctx, "qrdb_credential_store", map[string]any{
		"name":  name,
		"value": base64.StdEncoding.EncodeToString(value),
	})
	if err != nil {
		return &transport.RequestError{Method: "CredentialStore", Err: err}
	}
	return nil
}

// CredentialRetrieveBuffer returns the credential value for name in protected
// memory. The tool result is the wire DTO; it is moved into a buffer and the
// decoded slice wiped.
func (t *Transport) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_credential_retrieve", map[string]any{"name": name})
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieve", Err: err}
	}
	buf, decodeErr := transport.BufferFromBase64([]byte(text))
	if decodeErr != nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieve", Err: &valueDecodeError{Err: decodeErr}}
	}
	return buf, nil
}

// CredentialRetrieveVersionBuffer returns the stored value for a specific
// version in protected memory. The tool result is the wire DTO; it is moved
// into a buffer and the decoded slice wiped.
func (t *Transport) CredentialRetrieveVersionBuffer(ctx context.Context, req *transport.CredentialRetrieveVersionRequest) (*secure.Buffer, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: &nilRequestError{Method: "CredentialRetrieveVersion"}}
	}
	text, err := t.callTool(ctx, "qrdb_credential_retrieve_version", map[string]any{
		"name":    req.Name,
		"version": req.Version,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: err}
	}
	body := []byte(text)
	defer secure.Wipe(body)
	var resp struct {
		Value transport.WireValue `json:"value"`
	}
	if jsonErr := json.Unmarshal(body, &resp); jsonErr != nil {
		resp.Value.Buffer.Destroy()
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return resp.Value.Buffer, nil
}
