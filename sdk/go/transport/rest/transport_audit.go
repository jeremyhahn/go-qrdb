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
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/auditconv"
)

// Compile-time interface assertions.
var (
	_ transport.AuditClient   = (*Transport)(nil)
	_ transport.AuditProvider = (*Transport)(nil)
)

// AuditClient returns the REST transport's own AuditClient projection.
// Implements transport.AuditProvider so the SDK's Client.Audit() accessor
// resolves to a working client over REST (EmitAudit/QueryAudit are
// implemented directly on *Transport below).
func (t *Transport) AuditClient() transport.AuditClient {
	return t
}

// EmitAudit is not supported over the REST transport: audit events are
// emitted server-side by the request handlers themselves, and the REST API
// exposes no audit-emit endpoint. Remote callers query audit via QueryAudit;
// emission belongs to the in-process (embedded) transport. The error return
// satisfies the AuditClient contract for transport symmetry.
func (t *Transport) EmitAudit(ctx context.Context, ev transport.AuditEvent) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "EmitAudit"}
}

// QueryAudit queries the audit log via GET /v1/audit/events.
// Authorization: requires "audit:query" permission.
func (t *Transport) QueryAudit(ctx context.Context, f transport.AuditFilter) (*transport.AuditPage, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if !f.From.IsZero() {
		params.Set("from", f.From.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if !f.To.IsZero() {
		params.Set("to", f.To.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if f.Actor != "" {
		params.Set("actor", f.Actor)
	}
	if f.Resource != "" {
		params.Set("resource", f.Resource)
	}
	if f.Action != "" {
		params.Set("action", f.Action)
	}
	if f.Outcome != "" {
		params.Set("outcome", f.Outcome)
	}
	if f.TenantID != "" {
		params.Set("tenant_id", f.TenantID)
	}
	if f.Limit > 0 {
		params.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Cursor != "" {
		params.Set("cursor", f.Cursor)
	}
	if f.Replicated {
		params.Set("replicated", "true")
	}

	rawURL := t.baseURL + "/v1/audit/events"
	if len(params) > 0 {
		rawURL = rawURL + "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &transport.RequestError{
			Method: "QueryAudit",
			Err:    &transport.UnexpectedStatusError{StatusCode: resp.StatusCode, Body: string(body)},
		}
	}

	// The body is an audit.Page; decoding it as transport.AuditPage, whose
	// field names differ, dropped each event's tenant, client IP, request id
	// and chain hash.
	var page audit.Page
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, &transport.RequestError{Method: "QueryAudit", Err: err}
	}
	return auditconv.PageToTransport(&page), nil
}

// VerifyChain verifies the audit log's tamper-evidence hash chain via
// GET /v1/audit/verify. A non-2xx status means verification could not be
// PERFORMED; a 200 response with Verified=false is a TAMPER VERDICT, not an
// error -- see pkg/audit.Verify.
// Authorization: requires "audit:verify" permission.
func (t *Transport) VerifyChain(ctx context.Context) (*transport.AuditVerifyResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/audit/verify", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyChain", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyChain", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotImplemented {
		return nil, &transport.NotImplementedError{Method: "VerifyChain"}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &transport.RequestError{
			Method: "VerifyChain",
			Err:    &transport.UnexpectedStatusError{StatusCode: resp.StatusCode, Body: string(body)},
		}
	}

	var result transport.AuditVerifyResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &transport.RequestError{Method: "VerifyChain", Err: err}
	}
	return &result, nil
}

// VerifyReplicatedChain verifies the cluster-wide replicated audit chain's
// hash continuity for tenantID via GET /v1/audit/replicated/verify. A
// non-2xx status means verification could not be PERFORMED (including a
// tenant not entitled to the replicated chain, which answers 403); a 200
// response with Verified=false is a TAMPER VERDICT, not an error.
// Authorization: requires "audit:verify_replicated" permission.
func (t *Transport) VerifyReplicatedChain(ctx context.Context, tenantID string) (*transport.ChainContinuityResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	rawURL := t.baseURL + "/v1/audit/replicated/verify"
	if tenantID != "" {
		rawURL = rawURL + "?" + url.Values{"tenant_id": {tenantID}}.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyReplicatedChain", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "VerifyReplicatedChain", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotImplemented {
		return nil, &transport.NotImplementedError{Method: "VerifyReplicatedChain"}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &transport.RequestError{
			Method: "VerifyReplicatedChain",
			Err:    &transport.UnexpectedStatusError{StatusCode: resp.StatusCode, Body: string(body)},
		}
	}

	var result transport.ChainContinuityResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &transport.RequestError{Method: "VerifyReplicatedChain", Err: err}
	}
	return &result, nil
}
