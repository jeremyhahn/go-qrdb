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
