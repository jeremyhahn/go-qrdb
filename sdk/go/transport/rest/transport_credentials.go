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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CredentialClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CredentialClient implementation
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value.
// Maps to POST /v1/credentials/submit (204 No Content).
func (t *Transport) CredentialSubmit(ctx context.Context, req *transport.CredentialSubmitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return t.postJSONDecode(ctx, "CredentialSubmit", "/v1/credentials/submit", req, nil)
}

// CredentialStrategy returns the active credential management strategy.
// Maps to GET /v1/credentials/strategy.
func (t *Transport) CredentialStrategy(ctx context.Context) (*transport.CredentialStrategyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/credentials/strategy", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialStrategy", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialStrategy", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("CredentialStrategy", resp)
	}

	var strategyResp transport.CredentialStrategyResponse
	if err := json.NewDecoder(resp.Body).Decode(&strategyResp); err != nil {
		return nil, &transport.RequestError{Method: "CredentialStrategy", Err: &jsonDecodeError{Err: err}}
	}

	return &strategyResp, nil
}

// CredentialDelete removes a named credential.
// Maps to DELETE /v1/credentials/{name}.
func (t *Transport) CredentialDelete(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/credentials/" + url.PathEscape(name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "CredentialDelete", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "CredentialDelete", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("CredentialDelete", resp)
}

// CredentialList returns the sorted list of every stored credential
// name. Values are never returned by enumeration.
// Maps to GET /v1/credentials.
func (t *Transport) CredentialList(ctx context.Context, req *transport.CredentialListRequest) (*transport.CredentialListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// req is intentionally unused for now — reserved for future
	// filters per the wire contract.
	_ = req

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/credentials", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialList", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialList", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("CredentialList", resp)
	}

	var listResp transport.CredentialListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "CredentialList", Err: &jsonDecodeError{Err: err}}
	}
	return &listResp, nil
}

// CredentialRotate atomically replaces the current value of a credential
// and returns the previous + new version numbers plus the grace expiry.
// Maps to POST /v1/credentials/{name}/rotate.
func (t *Transport) CredentialRotate(ctx context.Context, req *transport.CredentialRotateRequest) (*transport.CredentialRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: &nilRequestError{}}
	}
	if req.Name == "" {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: &emptyCredentialNameError{}}
	}

	path := "/v1/credentials/" + url.PathEscape(req.Name) + "/rotate"
	var resp transport.CredentialRotateResponse
	if err := t.postJSONDecode(ctx, "CredentialRotate", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CredentialRetrieveVersion returns the credential value for a specific
// version. Version == 0 means "current version".
// Maps to GET /v1/credentials/{name}/versions/{version}.
func (t *Transport) CredentialRetrieveVersion(ctx context.Context, req *transport.CredentialRetrieveVersionRequest) (*transport.CredentialRetrieveVersionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: &nilRequestError{}}
	}
	if req.Name == "" {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: &emptyCredentialNameError{}}
	}

	reqURL := t.baseURL + "/v1/credentials/" + url.PathEscape(req.Name) + "/versions/" + strconv.FormatUint(req.Version, 10)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: err}
	}

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("CredentialRetrieveVersion", httpResp)
	}

	var resp transport.CredentialRetrieveVersionResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: &jsonDecodeError{Err: err}}
	}
	return &resp, nil
}

// CredentialListVersions returns the version history (current +
// non-expired grace) for a credential. Values are never returned.
// Maps to GET /v1/credentials/{name}/versions.
func (t *Transport) CredentialListVersions(ctx context.Context, req *transport.CredentialListVersionsRequest) (*transport.CredentialListVersionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: &nilRequestError{}}
	}
	if req.Name == "" {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: &emptyCredentialNameError{}}
	}

	reqURL := t.baseURL + "/v1/credentials/" + url.PathEscape(req.Name) + "/versions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: err}
	}

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("CredentialListVersions", httpResp)
	}

	var resp transport.CredentialListVersionsResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: &jsonDecodeError{Err: err}}
	}
	return &resp, nil
}

// CredentialGCExpired reaps historical versions whose grace period has
// elapsed and returns the count reaped.
// Maps to POST /v1/credentials/{name}/gc.
func (t *Transport) CredentialGCExpired(ctx context.Context, req *transport.CredentialGCRequest) (*transport.CredentialGCResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: &nilRequestError{}}
	}
	if req.Name == "" {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: &emptyCredentialNameError{}}
	}

	path := "/v1/credentials/" + url.PathEscape(req.Name) + "/gc"
	var resp transport.CredentialGCResponse
	if err := t.postJSONDecode(ctx, "CredentialGCExpired", path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CredentialStore persists a credential value. Maps to POST /v1/credentials/store.
func (t *Transport) CredentialStore(ctx context.Context, name string, value []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	body := map[string]string{"name": name, "value": string(value)}
	return t.postJSONDecode(ctx, "CredentialStore", "/v1/credentials/store", body, nil)
}

// CredentialRetrieve returns the credential value. Maps to
// GET /v1/credentials/{name}/value.
func (t *Transport) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp struct {
		Value string `json:"value"`
	}
	path := "/v1/credentials/" + url.PathEscape(name) + "/value"
	if err := t.getJSONDecode(ctx, "CredentialRetrieve", path, &resp); err != nil {
		return nil, err
	}
	return []byte(resp.Value), nil
}
