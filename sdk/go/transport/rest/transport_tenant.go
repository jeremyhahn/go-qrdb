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
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
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
	VolumePool    string                        `json:"volume_pool,omitempty"`
	Quotas        *transport.TenantQuotas       `json:"quotas,omitempty"`
	Entitlements  *transport.TenantEntitlements `json:"entitlements,omitempty"`
	// Refusal is set when the answering node will not serve this
	// tenant's data (docs/volume-pools/api.md's TenantRefusal); absent
	// otherwise. Another node may still serve it.
	Refusal      *transport.TenantRefusal `json:"refusal,omitempty"`
	CreatedAtStr string                   `json:"createdAt"`
	UpdatedAtStr string                   `json:"updatedAt"`
	CreatedAtAlt string                   `json:"created_at"`
	UpdatedAtAlt string                   `json:"updated_at"`
}

// restTenantBarrierInitBeginResponse is the on-wire JSON returned by the
// init/begin endpoint.
type restTenantBarrierInitBeginResponse struct {
	Nonce   []byte `json:"nonce,omitempty"`
	Expires int64  `json:"expires,omitempty"`
}

// restTenantBarrierInitRequest is the on-wire JSON for the init/complete
// endpoint. It matches the server-side handler payload (omitting the tenant
// ID, which is carried in the URL path).
type restTenantBarrierInitRequest struct {
	Passphrase []byte                             `json:"passphrase,omitempty"`
	Threshold  int                                `json:"threshold,omitempty"`
	Shares     int                                `json:"shares,omitempty"`
	Strategy   string                             `json:"strategy,omitempty"`
	Officers   []transport.OfficerRegisterRequest `json:"officers,omitempty"`
	Policy     string                             `json:"policy,omitempty"`
	Nonce      []byte                             `json:"nonce,omitempty"`
	PublicKey  []byte                             `json:"public_key,omitempty"`
	Signature  []byte                             `json:"signature,omitempty"`
	// InitStatement is the first admin's signed init statement
	// (barrierinit.SignTenantInit).
	InitStatement []byte `json:"init_statement,omitempty"`
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
	TenantID    string                              `json:"tenant_id"`
	Strategy    string                              `json:"strategy"`
	ShareCount  int                                 `json:"share_count,omitempty"`
	Threshold   int                                 `json:"threshold,omitempty"`
	TotalShares int                                 `json:"total_shares,omitempty"`
	Holders     []transport.TenantBarrierHolderCert `json:"holders,omitempty"`
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

	ClusterSeal *transport.ClusterSeal `json:"cluster_seal,omitempty"`
}

// toTenantInfo converts the flat REST payload into the transport-side
// TenantInfo. RFC3339 timestamp strings are parsed into time.Time; parse
// errors leave the field at its zero value.
func (r *restTenant) toTenantInfo() transport.TenantInfo {
	info := transport.TenantInfo{
		ID:            r.ID,
		Name:          r.Name,
		IsolationType: r.IsolationType,
		VolumePool:    r.VolumePool,
		Quotas:        r.Quotas,
		Entitlements:  r.Entitlements,
		Refusal:       r.Refusal,
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
	// The 201 body is the tenant's fields with its first admin alongside.
	var wire struct {
		restTenant
		Admin transport.TenantAdminCredential `json:"admin"`
	}
	if err := t.postJSONDecode(ctx, "CreateTenant", "/v1/tenants", req, &wire); err != nil {
		return nil, err
	}
	return &transport.CreateTenantResponse{Tenant: wire.toTenantInfo(), Admin: wire.Admin}, nil
}

// AddTrustAnchors registers trust anchors of req.TenantID. Maps to
// POST /v1/tenants/{id}/trust-anchors.
func (t *Transport) AddTrustAnchors(ctx context.Context, req *transport.AddTrustAnchorsRequest) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out transport.TrustAnchorList
	if err := t.postJSONDecode(ctx, "AddTrustAnchors", "/v1/tenants/"+url.PathEscape(req.TenantID)+"/trust-anchors", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTrustAnchors lists tenantID's trust anchors. Maps to
// GET /v1/tenants/{id}/trust-anchors.
func (t *Transport) ListTrustAnchors(ctx context.Context, tenantID string) (*transport.TrustAnchorList, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out transport.TrustAnchorList
	if err := t.getJSONDecode(ctx, "ListTrustAnchors", "/v1/tenants/"+url.PathEscape(tenantID)+"/trust-anchors", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveTrustAnchor withdraws one of tenantID's trust anchors. Maps to
// DELETE /v1/tenants/{id}/trust-anchors/{fingerprint}.
func (t *Transport) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.deleteNoContent(ctx, "RemoveTrustAnchor",
		"/v1/tenants/"+url.PathEscape(tenantID)+"/trust-anchors/"+url.PathEscape(fingerprint))
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

// tenantBarrierInitBegin calls POST /v1/tenants/{id}/barrier/init/begin and
// returns the nonce with the client-side TLS state of the connection that
// answered, which init/complete must reuse.
func (t *Transport) tenantBarrierInitBegin(ctx context.Context, client *http.Client, tenantID string) ([]byte, *tls.ConnectionState, error) {
	path := "/v1/tenants/" + url.PathEscape(tenantID) + "/barrier/init/begin"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, http.NoBody)
	if err != nil {
		return nil, nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, t.decodeErrorResponse("TenantBarrierInit", resp)
	}
	var wire restTenantBarrierInitBeginResponse
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &jsonDecodeError{Err: err}}
	}
	return wire.Nonce, resp.TLS, nil
}

// initProofClient returns the client tenant barrier init runs begin and
// complete on, and the function that releases it. The proof is bound to one
// connection's TLS exporter, so the client is a clone of the connection pool
// limited to a single connection per host: begin and complete then ride the
// same connection, and if that connection dies between them the server
// refuses the proof rather than accepting it elsewhere. A transport wired
// with a caller-supplied client (no pool of its own to clone) uses that
// client as is.
func (t *Transport) initProofClient() (*http.Client, func()) {
	if t.httpTransport == nil {
		return t.client, func() {}
	}
	pinned := t.httpTransport.Clone()
	pinned.MaxConnsPerHost = 1
	return &http.Client{
		Transport: transport.SOPINRoundTripper(pinned, "rest", transport.ConfiguredHost(t.baseURL)),
		Timeout:   t.client.Timeout,
	}, pinned.CloseIdleConnections
}

// TenantBarrierInit initializes a per-tenant barrier. Maps to
// POST /v1/tenants/{id}/barrier/init/begin, then .../init/complete carrying
// the proof of possession of req.Signer's key over the connection begin
// answered on -- the client's pooled HTTP/2 or HTTP/3 connection, which
// both requests share. For the Shamir strategy the server returns 200 with a
// JSON body counting the sealed shares; otherwise it returns 204 No Content
// and the response is populated from the request fields.
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
	client, release := t.initProofClient()
	defer release()
	nonce, cs, err := t.tenantBarrierInitBegin(ctx, client, req.TenantID)
	if err != nil {
		return nil, err
	}
	proof, err := transport.ProveTenantBarrierInit(req.Signer, req.TenantID, nonce, cs)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	statement, err := barrierinit.SignTenantInit(req.Signer, req.TenantID, req.Strategy)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	wireReq := restTenantBarrierInitRequest{
		Passphrase:    req.Passphrase,
		Threshold:     req.Threshold,
		Shares:        req.Shares,
		Strategy:      req.Strategy,
		Officers:      req.Officers,
		Policy:        req.Policy,
		Nonce:         proof.Nonce,
		PublicKey:     proof.PublicKey,
		Signature:     proof.Signature,
		InitStatement: statement,
	}
	data, err := json.Marshal(wireReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &jsonEncodeError{Err: err}}
	}
	path := "/v1/tenants/" + url.PathEscape(req.TenantID) + "/barrier/init/complete"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
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
		Holders:     wire.Holders,
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
	wireReq := restTenantBarrierUnsealRequest{Key: req.Key}
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
		ClusterSeal:     wire.ClusterSeal,
	}, nil
}

// tenantHolderPath returns the tenant barrier path for suffix, refusing an
// empty tenant id.
func tenantHolderPath(method, tenantID, suffix string) (string, error) {
	if tenantID == "" {
		return "", &transport.RequestError{Method: method, Err: &emptyTenantIDError{}}
	}
	return "/v1/tenants/" + url.PathEscape(tenantID) + "/barrier" + suffix, nil
}

// tenantHolderPost posts payload to the tenant barrier path for suffix.
func (t *Transport) tenantHolderPost(ctx context.Context, method, tenantID, suffix string, payload, dest any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	path, err := tenantHolderPath(method, tenantID, suffix)
	if err != nil {
		return err
	}
	return t.postJSONDecode(ctx, method, path, payload, dest)
}

// TenantBarrierOfficerRegister enrols a share holder of a tenant barrier.
// Maps to POST /v1/tenants/{id}/barrier/officers.
func (t *Transport) TenantBarrierOfficerRegister(ctx context.Context, req *transport.TenantOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRegister", Err: &emptyTenantIDError{}}
	}
	var resp transport.OfficerRegisterResponse
	if err := t.tenantHolderPost(ctx, "TenantBarrierOfficerRegister", req.TenantID, "/officers", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierOfficerList lists a tenant barrier's holders.
// Maps to GET /v1/tenants/{id}/barrier/officers.
func (t *Transport) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	path, err := tenantHolderPath("TenantBarrierOfficerList", tenantID, "/officers")
	if err != nil {
		return nil, err
	}
	var wire struct {
		Officers []*transport.OfficerStatus `json:"officers"`
	}
	if err := t.getJSONDecode(ctx, "TenantBarrierOfficerList", path, &wire); err != nil {
		return nil, err
	}
	return wire.Officers, nil
}

// TenantBarrierShareClaimBegin issues a claim nonce to a tenant holder.
// Maps to POST /v1/tenants/{id}/barrier/officers/{username}/claim/begin.
func (t *Transport) TenantBarrierShareClaimBegin(ctx context.Context, req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimBegin", Err: &emptyTenantIDError{}}
	}
	var resp transport.ShareClaimBeginResponse
	if err := t.tenantHolderPost(ctx, "TenantBarrierShareClaimBegin", req.TenantID, "/officers/"+url.PathEscape(req.Username)+"/claim/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierShareClaimComplete delivers a tenant holder's sealed envelope
// exactly once. Maps to
// POST /v1/tenants/{id}/barrier/officers/{username}/claim/complete.
func (t *Transport) TenantBarrierShareClaimComplete(ctx context.Context, req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierShareClaimComplete", Err: &emptyTenantIDError{}}
	}
	var resp transport.ShareClaimCompleteResponse
	if err := t.tenantHolderPost(ctx, "TenantBarrierShareClaimComplete", req.TenantID, "/officers/"+url.PathEscape(req.Username)+"/claim/complete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierQuorumBegin issues a tenant holder's quorum challenge.
// Maps to POST /v1/tenants/{id}/barrier/quorum/begin.
func (t *Transport) TenantBarrierQuorumBegin(ctx context.Context, req *transport.TenantQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierQuorumBegin", Err: &emptyTenantIDError{}}
	}
	var resp transport.QuorumChallenge
	if err := t.tenantHolderPost(ctx, "TenantBarrierQuorumBegin", req.TenantID, "/quorum/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierQuorumSubmit submits one authenticated tenant holder's share.
// Maps to POST /v1/tenants/{id}/barrier/quorum/submit.
func (t *Transport) TenantBarrierQuorumSubmit(ctx context.Context, req *transport.TenantQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierQuorumSubmit", Err: &emptyTenantIDError{}}
	}
	var resp transport.QuorumOutcome
	if err := t.tenantHolderPost(ctx, "TenantBarrierQuorumSubmit", req.TenantID, "/quorum/submit", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierRekeyStart opens a rekey round on a tenant barrier.
// Maps to POST /v1/tenants/{id}/barrier/rekey/start.
func (t *Transport) TenantBarrierRekeyStart(ctx context.Context, req *transport.TenantRekeyStartRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierRekeyStart", Err: &emptyTenantIDError{}}
	}
	var resp transport.QuorumRound
	if err := t.tenantHolderPost(ctx, "TenantBarrierRekeyStart", req.TenantID, "/rekey/start", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TenantBarrierOfficerRevoke revokes a tenant holder through a quorum-gated
// rekey. Maps to POST /v1/tenants/{id}/barrier/officers/revoke.
func (t *Transport) TenantBarrierOfficerRevoke(ctx context.Context, req *transport.TenantOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "TenantBarrierOfficerRevoke", Err: &emptyTenantIDError{}}
	}
	var resp transport.QuorumRound
	if err := t.tenantHolderPost(ctx, "TenantBarrierOfficerRevoke", req.TenantID, "/officers/revoke", req, &resp); err != nil {
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
