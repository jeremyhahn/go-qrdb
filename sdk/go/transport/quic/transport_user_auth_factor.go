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

package quic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion for the M14 user-side surface.
var _ transport.UserAuthFactorClient = (*Transport)(nil)

// quicUserFactorRecord matches the server-side wire shape.
type quicUserFactorRecord struct {
	ID             string            `json:"id"`
	UserID         uint64            `json:"user_id"`
	Type           string            `json:"type"`
	Label          string            `json:"label,omitempty"`
	Credential     []byte            `json:"credential"`
	SPKICredential []byte            `json:"spki_credential,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	RegisteredAt   string            `json:"registered_at"`
	LastUsedAt     string            `json:"last_used_at,omitempty"`
	RevokedAt      string            `json:"revoked_at,omitempty"`
	CreatedBy      string            `json:"created_by,omitempty"`
}

func userFactorFromQUICWire(r *quicUserFactorRecord) transport.AuthFactorRecord {
	out := transport.AuthFactorRecord{
		ID:             r.ID,
		UserID:         r.UserID,
		Type:           r.Type,
		Label:          r.Label,
		Credential:     r.Credential,
		SPKICredential: r.SPKICredential,
		Metadata:       r.Metadata,
		CreatedBy:      r.CreatedBy,
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

// AddAuthFactor invokes userauthfactor.Add via QUIC.
func (t *Transport) AddAuthFactor(ctx context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.Add", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Factor quicUserFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.AuthFactorAddResponse{Factor: userFactorFromQUICWire(&raw.Factor)}, nil
}

// ListAuthFactors invokes userauthfactor.List via QUIC.
func (t *Transport) ListAuthFactors(ctx context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.List", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Factors []quicUserFactorRecord `json:"factors"`
		Total   int                    `json:"total"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	out := transport.AuthFactorListResponse{
		Factors: make([]transport.AuthFactorRecord, len(raw.Factors)),
		Total:   raw.Total,
	}
	for i := range raw.Factors {
		out.Factors[i] = userFactorFromQUICWire(&raw.Factors[i])
	}
	return &out, nil
}

// GetAuthFactor invokes userauthfactor.Get via QUIC.
func (t *Transport) GetAuthFactor(ctx context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.Get", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Factor quicUserFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.AuthFactorGetResponse{Factor: userFactorFromQUICWire(&raw.Factor)}, nil
}

// RemoveAuthFactor invokes userauthfactor.Remove via QUIC.
func (t *Transport) RemoveAuthFactor(ctx context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.Remove", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.AuthFactorRemoveResponse{Status: raw.Status}, nil
}

// RotateAuthFactor invokes userauthfactor.Rotate via QUIC.
func (t *Transport) RotateAuthFactor(ctx context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.Rotate", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		NewFactor quicUserFactorRecord `json:"new_factor"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.AuthFactorRotateResponse{NewFactor: userFactorFromQUICWire(&raw.NewFactor)}, nil
}

// RelabelAuthFactor invokes userauthfactor.Relabel via QUIC.
func (t *Transport) RelabelAuthFactor(ctx context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.Relabel", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Factor quicUserFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.AuthFactorRelabelResponse{Factor: userFactorFromQUICWire(&raw.Factor)}, nil
}

// LoginAuthFactor invokes userauthfactor.Login via QUIC.
func (t *Transport) LoginAuthFactor(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	resp, err := t.call(ctx, "userauthfactor.Login", payload)
	if err != nil {
		return nil, err
	}
	var raw struct {
		UserID   uint64               `json:"user_id"`
		Username string               `json:"username"`
		Email    string               `json:"email,omitempty"`
		TenantID string               `json:"tenant_id,omitempty"`
		Roles    []string             `json:"roles,omitempty"`
		Active   bool                 `json:"active"`
		Factor   quicUserFactorRecord `json:"factor"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.AuthFactorLoginResponse{
		UserID:   raw.UserID,
		Username: raw.Username,
		Email:    raw.Email,
		TenantID: raw.TenantID,
		Roles:    raw.Roles,
		Active:   raw.Active,
		Factor:   userFactorFromQUICWire(&raw.Factor),
	}, nil
}
