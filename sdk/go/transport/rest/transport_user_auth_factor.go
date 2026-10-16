// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion for the M14 user-side surface.
var _ transport.UserAuthFactorClient = (*Transport)(nil)

// AddAuthFactor invokes POST /v1/users/factors.
func (t *Transport) AddAuthFactor(ctx context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "AddAuthFactor", Err: &jsonEncodeError{Err: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/v1/users/factors", bytes.NewReader(body))
	if err != nil {
		return nil, &transport.RequestError{Method: "AddAuthFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "AddAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("AddAuthFactor", resp)
	}
	var out transport.AuthFactorAddResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "AddAuthFactor", Err: err}
	}
	return &out, nil
}

// ListAuthFactors invokes GET /v1/users/factors/list?user_id=N.
func (t *Transport) ListAuthFactors(ctx context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	uri := t.baseURL + "/v1/users/factors/list?user_id=" + url.QueryEscape(strconv.FormatUint(req.UserID, 10))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListAuthFactors", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListAuthFactors", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListAuthFactors", resp)
	}
	var out transport.AuthFactorListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "ListAuthFactors", Err: err}
	}
	return &out, nil
}

// GetAuthFactor invokes GET /v1/users/factors/{factor_id}.
func (t *Transport) GetAuthFactor(ctx context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/v1/users/factors/"+url.PathEscape(req.FactorID), nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetAuthFactor", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetAuthFactor", resp)
	}
	var out transport.AuthFactorGetResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "GetAuthFactor", Err: err}
	}
	return &out, nil
}

// RemoveAuthFactor invokes DELETE /v1/users/factors/{factor_id}?force={bool}.
func (t *Transport) RemoveAuthFactor(ctx context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	uri := t.baseURL + "/v1/users/factors/" + url.PathEscape(req.FactorID)
	if req.Force {
		uri += "?force=true"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, uri, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "RemoveAuthFactor", Err: err}
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "RemoveAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("RemoveAuthFactor", resp)
	}
	var out transport.AuthFactorRemoveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "RemoveAuthFactor", Err: err}
	}
	return &out, nil
}

// DeleteAuthFactor issues POST /v1/users/factors/delete.
func (t *Transport) DeleteAuthFactor(ctx context.Context, req *transport.AuthFactorDeleteRequest) (*transport.AuthFactorDeleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "DeleteAuthFactor", Err: &jsonEncodeError{Err: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/v1/users/factors/delete", bytes.NewReader(body))
	if err != nil {
		return nil, &transport.RequestError{Method: "DeleteAuthFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "DeleteAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("DeleteAuthFactor", resp)
	}
	var out transport.AuthFactorDeleteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "DeleteAuthFactor", Err: err}
	}
	return &out, nil
}

// RotateAuthFactor invokes POST /v1/users/factors/rotate.
func (t *Transport) RotateAuthFactor(ctx context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := req.Validate(); err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: err}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: &jsonEncodeError{Err: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/v1/users/factors/rotate", bytes.NewReader(body))
	if err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("RotateAuthFactor", resp)
	}
	var out transport.AuthFactorRotateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: err}
	}
	return &out, nil
}

// RelabelAuthFactor invokes PUT /v1/users/factors/{factor_id}/label.
func (t *Transport) RelabelAuthFactor(ctx context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "RelabelAuthFactor", Err: &jsonEncodeError{Err: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, t.baseURL+"/v1/users/factors/"+url.PathEscape(req.FactorID)+"/label", bytes.NewReader(body))
	if err != nil {
		return nil, &transport.RequestError{Method: "RelabelAuthFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "RelabelAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("RelabelAuthFactor", resp)
	}
	var out transport.AuthFactorRelabelResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "RelabelAuthFactor", Err: err}
	}
	return &out, nil
}

// LoginAuthFactor invokes POST /v1/users/factors/login.
func (t *Transport) LoginAuthFactor(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "LoginAuthFactor", Err: &jsonEncodeError{Err: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/v1/users/factors/login", bytes.NewReader(body))
	if err != nil {
		return nil, &transport.RequestError{Method: "LoginAuthFactor", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "LoginAuthFactor", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("LoginAuthFactor", resp)
	}
	var out transport.AuthFactorLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &transport.RequestError{Method: "LoginAuthFactor", Err: err}
	}
	return &out, nil
}
