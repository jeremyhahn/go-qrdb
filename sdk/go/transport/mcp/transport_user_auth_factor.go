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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion for the M14 user-side surface.
var _ transport.UserAuthFactorClient = (*Transport)(nil)

// mcpFactorRecord matches the server-side MCP tool response shape, where
// timestamps are RFC3339 strings and the credential is base64-encoded.
type mcpFactorRecord struct {
	ID           string            `json:"id"`
	UserID       uint64            `json:"user_id"`
	Type         string            `json:"type"`
	Label        string            `json:"label,omitempty"`
	Credential   string            `json:"credential"` // base64-encoded
	Metadata     map[string]string `json:"metadata,omitempty"`
	RegisteredAt string            `json:"registered_at"`
	LastUsedAt   string            `json:"last_used_at,omitempty"`
	RevokedAt    string            `json:"revoked_at,omitempty"`
	CreatedBy    string            `json:"created_by,omitempty"`
}

func mcpRecordToTransport(r *mcpFactorRecord) transport.AuthFactorRecord {
	out := transport.AuthFactorRecord{
		ID:        r.ID,
		UserID:    r.UserID,
		Type:      r.Type,
		Label:     r.Label,
		Metadata:  r.Metadata,
		CreatedBy: r.CreatedBy,
	}
	if r.Credential != "" {
		decoded, err := base64.StdEncoding.DecodeString(r.Credential)
		if err == nil {
			out.Credential = decoded
		}
	}
	out.RegisteredAt, _ = time.Parse(time.RFC3339, r.RegisteredAt)
	if r.LastUsedAt != "" {
		if ts, err := time.Parse(time.RFC3339, r.LastUsedAt); err == nil {
			out.LastUsedAt = &ts
		}
	}
	if r.RevokedAt != "" {
		if ts, err := time.Parse(time.RFC3339, r.RevokedAt); err == nil {
			out.RevokedAt = &ts
		}
	}
	return out
}

// AddAuthFactor invokes qrdb_user_authfactor_add.
func (t *Transport) AddAuthFactor(ctx context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	args := map[string]any{
		"user_id":        strconv.FormatUint(req.UserID, 10),
		"type":           req.Type,
		"label":          req.Label,
		"credential_b64": base64.StdEncoding.EncodeToString(req.Credential),
		"created_by":     req.CreatedBy,
	}
	if len(req.Metadata) > 0 {
		mj, _ := json.Marshal(req.Metadata)
		args["metadata_json"] = string(mj)
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_add", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "AddAuthFactor", Err: err}
	}
	var raw struct {
		Factor mcpFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "AddAuthFactor", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.AuthFactorAddResponse{Factor: mcpRecordToTransport(&raw.Factor)}, nil
}

// ListAuthFactors invokes qrdb_user_authfactor_list.
func (t *Transport) ListAuthFactors(ctx context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_list", map[string]any{
		"user_id": strconv.FormatUint(req.UserID, 10),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListAuthFactors", Err: err}
	}
	var raw struct {
		Factors []mcpFactorRecord `json:"factors"`
		Total   int               `json:"total"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "ListAuthFactors", Err: &jsonDecodeError{Err: err}}
	}
	out := transport.AuthFactorListResponse{
		Factors: make([]transport.AuthFactorRecord, len(raw.Factors)),
		Total:   raw.Total,
	}
	for i := range raw.Factors {
		out.Factors[i] = mcpRecordToTransport(&raw.Factors[i])
	}
	return &out, nil
}

// GetAuthFactor invokes qrdb_user_authfactor_get.
func (t *Transport) GetAuthFactor(ctx context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_get", map[string]any{
		"factor_id": req.FactorID,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetAuthFactor", Err: err}
	}
	var raw struct {
		Factor mcpFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "GetAuthFactor", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.AuthFactorGetResponse{Factor: mcpRecordToTransport(&raw.Factor)}, nil
}

// RemoveAuthFactor invokes qrdb_user_authfactor_remove.
func (t *Transport) RemoveAuthFactor(ctx context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_remove", map[string]any{
		"factor_id": req.FactorID,
		"force":     req.Force,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RemoveAuthFactor", Err: err}
	}
	var raw struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "RemoveAuthFactor", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.AuthFactorRemoveResponse{Status: raw.Status}, nil
}

// RotateAuthFactor invokes qrdb_user_authfactor_rotate.
func (t *Transport) RotateAuthFactor(ctx context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	args := map[string]any{
		"old_factor_id":  req.OldFactorID,
		"type":           req.NewFactor.Type,
		"label":          req.NewFactor.Label,
		"credential_b64": base64.StdEncoding.EncodeToString(req.NewFactor.Credential),
	}
	if len(req.NewFactor.Metadata) > 0 {
		mj, _ := json.Marshal(req.NewFactor.Metadata)
		args["metadata_json"] = string(mj)
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_rotate", args)
	if err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: err}
	}
	var raw struct {
		Factor mcpFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "RotateAuthFactor", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.AuthFactorRotateResponse{NewFactor: mcpRecordToTransport(&raw.Factor)}, nil
}

// RelabelAuthFactor invokes qrdb_user_authfactor_relabel.
func (t *Transport) RelabelAuthFactor(ctx context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_relabel", map[string]any{
		"factor_id": req.FactorID,
		"label":     req.Label,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "RelabelAuthFactor", Err: err}
	}
	var raw struct {
		Factor mcpFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "RelabelAuthFactor", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.AuthFactorRelabelResponse{Factor: mcpRecordToTransport(&raw.Factor)}, nil
}

// LoginAuthFactor invokes qrdb_user_authfactor_login.
func (t *Transport) LoginAuthFactor(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, "qrdb_user_authfactor_login", map[string]any{
		"type":           req.Type,
		"lookup_key_b64": base64.StdEncoding.EncodeToString(req.LookupKey),
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "LoginAuthFactor", Err: err}
	}
	var raw struct {
		UserID   uint64          `json:"user_id"`
		Username string          `json:"username"`
		Email    string          `json:"email,omitempty"`
		TenantID string          `json:"tenant_id,omitempty"`
		Roles    []string        `json:"roles,omitempty"`
		Active   bool            `json:"active"`
		Factor   mcpFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, &transport.RequestError{Method: "LoginAuthFactor", Err: &jsonDecodeError{Err: err}}
	}
	return &transport.AuthFactorLoginResponse{
		UserID:   raw.UserID,
		Username: raw.Username,
		Email:    raw.Email,
		TenantID: raw.TenantID,
		Roles:    raw.Roles,
		Active:   raw.Active,
		Factor:   mcpRecordToTransport(&raw.Factor),
	}, nil
}
