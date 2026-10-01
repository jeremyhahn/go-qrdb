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
	"strconv"
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
	DataRoot      string                        `json:"data_root,omitempty"`
	Quotas        *transport.TenantQuotas       `json:"quotas,omitempty"`
	Entitlements  *transport.TenantEntitlements `json:"entitlements,omitempty"`
	CreatedAtStr  string                        `json:"createdAt"`
	UpdatedAtStr  string                        `json:"updatedAt"`
	CreatedAtAlt  string                        `json:"created_at"`
	UpdatedAtAlt  string                        `json:"updated_at"`
}

// restTenantBarrierInitRequest is the on-wire JSON for the init endpoint.
// It matches the server-side handler payload (omitting the tenant ID, which
// is carried in the URL path).
type restTenantBarrierInitRequest struct {
	Passphrase []byte `json:"passphrase,omitempty"`
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

// restTenantBarrierInitResponse is the on-wire JSON returned by the init
// endpoint for the Shamir strategy. For none/passphrase the server returns
// 204 No Content and this struct is never decoded.
type restTenantBarrierInitResponse struct {
	TenantID    string `json:"tenant_id"`
	Strategy    string `json:"strategy"`
	ShareCount  int    `json:"share_count,omitempty"`
	Threshold   int    `json:"threshold,omitempty"`
	TotalShares int    `json:"total_shares,omitempty"`
}

// restTenantBarrierUnsealShareRequest is the on-wire JSON for the
// unseal-share endpoint. Share carries a single Shamir share string.
type restTenantBarrierUnsealShareRequest struct {
	Share string `json:"share"`
}

// restTenantBarrierUnsealProgress is the on-wire JSON returned by the
// unseal-share endpoint.
type restTenantBarrierUnsealProgress struct {
	TenantID  string `json:"tenant_id"`
	Required  int    `json:"required"`
	Submitted int    `json:"submitted"`
	Complete  bool   `json:"complete"`
	Sealed    bool   `json:"sealed"`
}

// restTenantBarrierStatus is the on-wire JSON returned by the status endpoint.
// It mirrors tenant.TenantBarrierStatus on the server side.
type restTenantBarrierStatus struct {
	TenantID        string `json:"tenant_id"`
	Sealed          bool   `json:"sealed"`
	Initialized     bool   `json:"initialized"`
	Strategy        string `json:"strategy"`
	HardwareBacked  bool   `json:"hardware_backed"`
	ShamirThreshold int    `json:"shamir_threshold,omitempty"`
	ShamirTotal     int    `json:"shamir_total,omitempty"`
}

// toTenantInfo converts the flat REST payload into the transport-side
// TenantInfo. RFC3339 timestamp strings are parsed into time.Time; parse
// errors leave the field at its zero value.
func (r *restTenant) toTenantInfo() transport.TenantInfo {
	info := transport.TenantInfo{
		ID:            r.ID,
		Name:          r.Name,
		IsolationType: r.IsolationType,
		DataRoot:      r.DataRoot,
		Quotas:        r.Quotas,
		Entitlements:  r.Entitlements,
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

// ListDeletedTenants returns one page of retired tenant ids. Maps to
// GET /v1/tenants/deleted/list?page_size=&page_token=.
func (t *Transport) ListDeletedTenants(ctx context.Context, req *transport.ListDeletedTenantsRequest) (*transport.ListDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		req = &transport.ListDeletedTenantsRequest{}
	}
	q := url.Values{}
	if req.PageSize != 0 {
		q.Set("page_size", strconv.FormatInt(int64(req.PageSize), 10))
	}
	if req.PageToken != "" {
		q.Set("page_token", req.PageToken)
	}
	reqURL := t.baseURL + "/v1/tenants/deleted/list"
	if len(q) > 0 {
		reqURL += "?" + q.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListDeletedTenants", resp)
	}
	out := &transport.ListDeletedTenantsResponse{}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, &transport.RequestError{Method: "ListDeletedTenants", Err: &jsonDecodeError{Err: err}}
	}
	if out.Tenants == nil {
		out.Tenants = []transport.DeletedTenant{}
	}
	return out, nil
}

// ReconcileDeletedTenants runs the deleted-tenant reconcile on the node serving
// the request. Maps to POST /v1/tenants/deleted/reconcile.
func (t *Transport) ReconcileDeletedTenants(ctx context.Context) (*transport.ReconcileDeletedTenantsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/v1/tenants/deleted/reconcile", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ReconcileDeletedTenants", resp)
	}
	out := &transport.ReconcileDeletedTenantsResponse{}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, &transport.RequestError{Method: "ReconcileDeletedTenants", Err: &jsonDecodeError{Err: err}}
	}
	return out, transport.ReconcileFailure(out, nil)
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

// UpdateTenantEntitlements sets the feature entitlements for a tenant.
// Maps to PATCH /v1/tenants/{id}/entitlements.
func (t *Transport) UpdateTenantEntitlements(ctx context.Context, req *transport.UpdateTenantEntitlementsRequest) (*transport.UpdateTenantEntitlementsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	data, err := json.Marshal(&req.Entitlements)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: &jsonEncodeError{Err: err}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/entitlements"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("UpdateTenantEntitlements", resp)
	}
	var wire restTenant
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "UpdateTenantEntitlements", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.UpdateTenantEntitlementsResponse{Tenant: wire.toTenantInfo()}, nil
}

// TenantBarrierInit initializes a per-tenant barrier.
// Maps to POST /v1/tenants/{id}/barrier/init.
// For the Shamir strategy the server returns 200 with a JSON body carrying the
// generated shares; for none/passphrase it returns 204 No Content and the
// response is populated from the request fields.
func (t *Transport) TenantBarrierInit(ctx context.Context, req *transport.TenantBarrierInitRequest) (*transport.TenantBarrierInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &nilRequestError{}}
	}
	if req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &emptyTenantIDError{}}
	}
	wireReq := restTenantBarrierInitRequest{
		Passphrase: req.Passphrase,
		Threshold:  req.Threshold,
		Shares:     req.Shares,
		Strategy:   req.Strategy,
	}
	data, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &jsonEncodeError{Err: err}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/init"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNoContent {
		// none/passphrase strategy: no body, populate from request.
		return &transport.TenantBarrierInitResponse{TenantID: req.TenantID, Strategy: req.Strategy}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TenantBarrierInit", resp)
	}
	var wire restTenantBarrierInitResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.TenantBarrierInitResponse{
		TenantID:    wire.TenantID,
		Strategy:    wire.Strategy,
		ShareCount:  wire.ShareCount,
		Threshold:   wire.Threshold,
		TotalShares: wire.TotalShares,
	}, nil
}

// TenantBarrierUnsealShare submits a single Shamir share toward a per-tenant
// barrier's quorum threshold.
// Maps to POST /v1/tenants/{id}/barrier/unseal-share.
func (t *Transport) TenantBarrierUnsealShare(ctx context.Context, req *transport.TenantBarrierUnsealShareRequest) (*transport.TenantBarrierUnsealProgress, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: &nilRequestError{}}
	}
	if req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: &emptyTenantIDError{}}
	}
	wireReq := restTenantBarrierUnsealShareRequest{Share: req.Share}
	data, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: &jsonEncodeError{Err: err}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/unseal-share"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TenantBarrierUnsealShare", resp)
	}
	var wire restTenantBarrierUnsealProgress
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierUnsealShare", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.TenantBarrierUnsealProgress{
		TenantID:  wire.TenantID,
		Required:  wire.Required,
		Submitted: wire.Submitted,
		Complete:  wire.Complete,
		Sealed:    wire.Sealed,
	}, nil
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
		TenantID:        tenantID,
		Sealed:          wire.Sealed,
		Initialized:     wire.Initialized,
		Strategy:        wire.Strategy,
		HardwareBacked:  wire.HardwareBacked,
		ShamirThreshold: wire.ShamirThreshold,
		ShamirTotal:     wire.ShamirTotal,
	}, nil
}

// TenantBarrierOfficerRegister registers a Security Officer for the named
// tenant's barrier. Maps to POST /v1/tenants/{id}/barrier/officers.
//
// csr_pem/cert_pem travel over the wire as plain PEM strings (see
// officerRegisterWireRequest/officerRegisterWireResponse in
// transport_barrier.go); transport.TenantOfficerRegisterRequest.CSRPEM and
// transport.OfficerRegisterResponse.CertPEM are typed []byte for the other
// transports, so they are converted here rather than marshaled directly.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil || req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/officers"
	wireReq := &officerRegisterWireRequest{
		Username:          req.Username,
		CSRPEM:            string(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	}
	var wireResp officerRegisterWireResponse
	if err := t.postJSONDecode(ctx, "TenantBarrierOfficerRegister", path, wireReq, &wireResp); err != nil {
		return nil, err
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    []byte(wireResp.CertPEM),
		ShareIndex: wireResp.ShareIndex,
	}, nil
}

// TenantBarrierOfficerList returns all enrolled Security Officers for the
// named tenant's barrier. Maps to GET /v1/tenants/{id}/barrier/officers.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if tenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(tenantID) + "/barrier/officers"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TenantBarrierOfficerList", resp)
	}
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerList", Err: &jsonDecodeError{Err: err}}
	}
	return wire.Officers, nil
}

// TenantBarrierOfficerClaimStatus returns a per-officer claim status snapshot
// for the named tenant's barrier.
// Maps to GET /v1/tenants/{id}/barrier/officers/claim-status.
func (t *Transport) TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if tenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(tenantID) + "/barrier/officers/claim-status"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("TenantBarrierOfficerClaimStatus", resp)
	}
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerClaimStatus", Err: &jsonDecodeError{Err: err}}
	}
	return wire.Officers, nil
}

// TenantBarrierShareClaimBegin issues a challenge nonce to the named Security
// Officer for the tenant barrier share claim handshake.
// Maps to POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil || req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/officers/" + url.PathEscape(req.Username) + "/claim/begin"
	var resp transport.ShareClaimBeginResponse
	if err := t.postJSONDecode(ctx, "TenantBarrierShareClaimBegin", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierShareClaimComplete verifies the Security Officer's nonce
// signature and returns the tenant barrier share bytes exactly once.
// Maps to POST /v1/tenants/{id}/barrier/officers/{username}/claim/complete.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil || req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/officers/" + url.PathEscape(req.Username) + "/claim/complete"
	var resp transport.ShareClaimCompleteResponse
	if err := t.postJSONDecode(ctx, "TenantBarrierShareClaimComplete", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierShamirSealVote submits a Shamir share as a seal vote for the
// named tenant's barrier. Maps to POST /v1/tenants/{id}/barrier/seal-vote.
func (t *Transport) TenantBarrierShamirSealVote(ctx context.Context, req *transport.TenantShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil || req.TenantID == "" {
		return nil, &transport.RequestError{Method: "TenantBarrierShamirSealVote", Err: &emptyTenantIDError{}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/seal-vote"
	var resp transport.ShamirSealVoteResponse
	if err := t.postJSONDecode(ctx, "TenantBarrierShamirSealVote", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
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
