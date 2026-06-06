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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TenantClient = (*Transport)(nil)

// restTenant is the on-wire JSON shape returned by the REST tenant handlers
// (flat fields, mixed-case timestamp keys). We decode into this shape then
// lift the fields into the transport's TenantInfo contract.
type restTenant struct {
	ID            string                        `json:"id"`
	Name          string                        `json:"name"`
	IsolationType transport.TenantIsolationType `json:"isolation_type,omitempty"`
	Quotas        *transport.TenantQuotas       `json:"quotas,omitempty"`
	CreatedAtStr  string                        `json:"createdAt"`
	UpdatedAtStr  string                        `json:"updatedAt"`
	CreatedAtAlt  string                        `json:"created_at"`
	UpdatedAtAlt  string                        `json:"updated_at"`
}

// restTenantBarrierInitRequest is the on-wire JSON for the init endpoint.
// It matches the server-side handler payload (omitting the tenant ID, which
// is carried in the URL path).
type restTenantBarrierInitRequest struct {
	Passphrase string `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
}

// restTenantBarrierUnsealRequest is the on-wire JSON for the unseal endpoint.
// Go's encoding/json base64-encodes []byte, which matches the server's
// expected wire format.
type restTenantBarrierUnsealRequest struct {
	Key []byte `json:"key,omitempty"`
}

// restTenantBarrierStatus is the on-wire JSON returned by the status endpoint.
// It mirrors tenant.TenantBarrierStatus on the server side.
type restTenantBarrierStatus struct {
	TenantID       string `json:"tenant_id"`
	Sealed         bool   `json:"sealed"`
	Initialized    bool   `json:"initialized"`
	Strategy       string `json:"strategy"`
	HardwareBacked bool   `json:"hardware_backed"`
}

// toTenantInfo converts the flat REST payload into the transport-side
// TenantInfo. RFC3339 timestamp strings are parsed into time.Time; parse
// errors leave the field at its zero value.
func (r *restTenant) toTenantInfo() transport.TenantInfo {
	info := transport.TenantInfo{
		ID:            r.ID,
		Name:          r.Name,
		IsolationType: r.IsolationType,
		Quotas:        r.Quotas,
	}
	if ts := pickFirstNonEmpty(r.CreatedAtStr, r.CreatedAtAlt); ts != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			info.CreatedAt = parsed
		}
	}
	if ts := pickFirstNonEmpty(r.UpdatedAtStr, r.UpdatedAtAlt); ts != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			info.UpdatedAt = parsed
		}
	}
	return info
}

func pickFirstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// CreateTenant creates a new tenant. Maps to POST /v1/tenants.
func (t *Transport) CreateTenant(ctx context.Context, req *transport.CreateTenantRequest) (*transport.CreateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restTenant
	if err := t.postJSONDecode(ctx, "CreateTenant", "/v1/tenants", req, &wire); err != nil {
		return nil, err
	}
	return &transport.CreateTenantResponse{Tenant: wire.toTenantInfo()}, nil
}

// GetTenant retrieves a tenant by ID. Maps to GET /v1/tenants/{id}.
func (t *Transport) GetTenant(ctx context.Context, tenantID string) (*transport.GetTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	reqURL := t.baseURL + "/v1/tenants/" + url.PathEscape(tenantID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetTenant", resp)
	}
	var wire restTenant
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "GetTenant", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.GetTenantResponse{Tenant: wire.toTenantInfo()}, nil
}

// ListTenants returns all tenants. Maps to GET /v1/tenants/list.
func (t *Transport) ListTenants(ctx context.Context) (*transport.ListTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/tenants/list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListTenants", resp)
	}
	var wire struct {
		Tenants []*restTenant `json:"tenants"`
		Total   int           `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "ListTenants", Err: &jsonDecodeError{Err: err}}
	}
	out := &transport.ListTenantsResponse{
		Tenants: make([]transport.TenantInfo, 0, len(wire.Tenants)),
	}
	for _, rt := range wire.Tenants {
		if rt == nil {
			continue
		}
		out.Tenants = append(out.Tenants, rt.toTenantInfo())
	}
	return out, nil
}

// DeleteTenant deletes a tenant by ID. Maps to DELETE /v1/tenants/{id}.
func (t *Transport) DeleteTenant(ctx context.Context, tenantID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	reqURL := t.baseURL + "/v1/tenants/" + url.PathEscape(tenantID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "DeleteTenant", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DeleteTenant", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return t.decodeErrorResponse("DeleteTenant", resp)
}

// UpdateTenant applies a partial update to an existing tenant.
// Maps to PUT /v1/tenants/{id}.
func (t *Transport) UpdateTenant(ctx context.Context, req *transport.UpdateTenantRequest) (*transport.UpdateTenantResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restTenant
	if err := t.putJSONDecode(ctx, "UpdateTenant", "/v1/tenants/"+url.PathEscape(req.TenantID), req, &wire); err != nil {
		return nil, err
	}
	return &transport.UpdateTenantResponse{Tenant: wire.toTenantInfo()}, nil
}

// UpdateTenantQuotas sets the resource quotas for a tenant.
// Maps to POST /v1/tenants/{id}/quotas.
func (t *Transport) UpdateTenantQuotas(ctx context.Context, req *transport.UpdateTenantQuotasRequest) (*transport.UpdateTenantQuotasResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var wire restTenant
	if err := t.postJSONDecode(ctx, "UpdateTenantQuotas", "/v1/tenants/"+url.PathEscape(req.TenantID)+"/quotas", &req.Quotas, &wire); err != nil {
		return nil, err
	}
	return &transport.UpdateTenantQuotasResponse{Tenant: wire.toTenantInfo()}, nil
}

// TenantBarrierInit initializes a per-tenant barrier.
// Maps to POST /v1/tenants/{id}/barrier/init.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierInit", Err: &nilRequestError{}}
	}
	if req.TenantID == "" {
		return &transport.RequestError{Method: "TenantBarrierInit", Err: &emptyTenantIDError{}}
	}
	wireReq := restTenantBarrierInitRequest{
		Passphrase: req.Passphrase,
		Threshold:  req.Threshold,
		Shares:     req.Shares,
		Strategy:   req.Strategy,
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/init"
	return t.postJSONNoContent(ctx, "TenantBarrierInit", path, wireReq)
}

// TenantBarrierUnseal unseals a per-tenant barrier.
// Maps to POST /v1/tenants/{id}/barrier/unseal.
func (t *Transport) TenantBarrierUnseal(ctx context.Context, req *transport.TenantBarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: &nilRequestError{}}
	}
	if req.TenantID == "" {
		return &transport.RequestError{Method: "TenantBarrierUnseal", Err: &emptyTenantIDError{}}
	}
	// Prefer Key when set; fall back to Share for callers that use the
	// Shamir-share field. Either way the server only needs raw bytes.
	key := req.Key
	if len(key) == 0 {
		key = req.Share
	}
	wireReq := restTenantBarrierUnsealRequest{Key: key}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/unseal"
	return t.postJSONNoContent(ctx, "TenantBarrierUnseal", path, wireReq)
}

// TenantBarrierSeal seals a per-tenant barrier.
// Maps to POST /v1/tenants/{id}/barrier/seal.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: &nilRequestError{}}
	}
	if req.TenantID == "" {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/seal"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, nil)
	if err != nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "TenantBarrierSeal", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return t.decodeErrorResponse("TenantBarrierSeal", resp)
}

// TenantBarrierStatus reports the current sealed/initialized state and active
// strategy metadata for a per-tenant barrier.
// Maps to GET /v1/tenants/{id}/barrier/status.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &nilRequestError{}}
	}
	if req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/status"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TenantBarrierStatus", resp)
	}
	var wire restTenantBarrierStatus
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierStatus", Err: &jsonDecodeError{Err: err}}
	}
	tenantID := wire.TenantID
	if tenantID == "" {
		tenantID = req.TenantID
	}
	return &transport.TenantBarrierStatusResponse{
		TenantID:       tenantID,
		Sealed:         wire.Sealed,
		Initialized:    wire.Initialized,
		Strategy:       wire.Strategy,
		HardwareBacked: wire.HardwareBacked,
	}, nil
}

// postJSONNoContent encodes payload as JSON, POSTs it to path, and accepts
// 200 OK or 204 No Content as success. Any other status decodes the server
// error response.
func (t *Transport) postJSONNoContent(ctx context.Context, method, path string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return t.decodeErrorResponse(method, resp)
}
