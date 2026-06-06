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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ---------------------------------------------------------------------------
// User auth factor QUIC frame handlers (M14)
//
// All frames use JSON-encoded payloads. Frame method names:
//   userauthfactor.Add, userauthfactor.List, userauthfactor.Get,
//   userauthfactor.Remove, userauthfactor.Rotate, userauthfactor.Relabel,
//   userauthfactor.Login
// ---------------------------------------------------------------------------

// quicUserFactorRecord is the wire shape of an auth.AuthFactor.
type quicUserFactorRecord struct {
	ID           string            `json:"id"`
	UserID       uint64            `json:"user_id"`
	Type         string            `json:"type"`
	Label        string            `json:"label,omitempty"`
	Credential   []byte            `json:"credential"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	RegisteredAt string            `json:"registered_at"`
	LastUsedAt   string            `json:"last_used_at,omitempty"`
	RevokedAt    string            `json:"revoked_at,omitempty"`
	CreatedBy    string            `json:"created_by,omitempty"`
}

type quicUserFactorAddRequest struct {
	UserID     uint64            `json:"user_id"`
	Type       string            `json:"type"`
	Label      string            `json:"label,omitempty"`
	Credential []byte            `json:"credential"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedBy  string            `json:"created_by,omitempty"`
}

type quicUserFactorListRequest struct {
	UserID uint64 `json:"user_id"`
}

type quicUserFactorGetRequest struct {
	FactorID string `json:"factor_id"`
}

type quicUserFactorRemoveRequest struct {
	FactorID string `json:"factor_id"`
	Force    bool   `json:"force,omitempty"`
}

type quicUserFactorRotateRequest struct {
	OldFactorID string                   `json:"old_factor_id"`
	NewFactor   quicUserFactorAddRequest `json:"new_factor"`
}

type quicUserFactorRelabelRequest struct {
	FactorID string `json:"factor_id"`
	Label    string `json:"label"`
}

type quicUserFactorLoginRequest struct {
	Type      string `json:"type"`
	LookupKey []byte `json:"lookup_key"`
}

// userFactorToQUIC projects an auth.AuthFactor into the QUIC wire shape.
func userFactorToQUIC(f *auth.AuthFactor) quicUserFactorRecord {
	if f == nil {
		return quicUserFactorRecord{}
	}
	r := quicUserFactorRecord{
		ID:           f.ID,
		UserID:       f.UserID,
		Type:         string(f.Type),
		Label:        f.Label,
		Credential:   f.Credential,
		Metadata:     f.Metadata,
		RegisteredAt: f.RegisteredAt.Format(time.RFC3339),
		CreatedBy:    f.CreatedBy,
	}
	if f.LastUsedAt != nil {
		r.LastUsedAt = f.LastUsedAt.Format(time.RFC3339)
	}
	if f.RevokedAt != nil {
		r.RevokedAt = f.RevokedAt.Format(time.RFC3339)
	}
	return r
}

func userFactorFromQUIC(req *quicUserFactorAddRequest) *auth.AuthFactor {
	return &auth.AuthFactor{
		UserID:     req.UserID,
		Type:       auth.AuthFactorType(req.Type),
		Label:      req.Label,
		Credential: req.Credential,
		Metadata:   req.Metadata,
		CreatedBy:  req.CreatedBy,
	}
}

func (s *Server) handleUserAuthFactorAdd(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorAddRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	factor := userFactorFromQUIC(&req)
	if err := s.userAuthFactorSvc.Add(ctx, req.UserID, factor); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"factor": userFactorToQUIC(factor)})
}

func (s *Server) handleUserAuthFactorList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorListRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	factors, err := s.userAuthFactorSvc.List(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	wire := make([]quicUserFactorRecord, len(factors))
	for i, f := range factors {
		wire[i] = userFactorToQUIC(f)
	}
	return json.Marshal(map[string]any{
		"factors": wire,
		"total":   len(wire),
	})
}

func (s *Server) handleUserAuthFactorGet(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorGetRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	factor, err := s.userAuthFactorSvc.Get(ctx, req.FactorID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"factor": userFactorToQUIC(factor)})
}

func (s *Server) handleUserAuthFactorRemove(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorRemoveRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.userAuthFactorSvc.Remove(ctx, req.FactorID, req.Force); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"status": "revoked"})
}

func (s *Server) handleUserAuthFactorRotate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorRotateRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	newFactor := userFactorFromQUIC(&req.NewFactor)
	if err := s.userAuthFactorSvc.Rotate(ctx, req.OldFactorID, newFactor); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"new_factor": userFactorToQUIC(newFactor)})
}

func (s *Server) handleUserAuthFactorRelabel(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorRelabelRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.userAuthFactorSvc.Relabel(ctx, req.FactorID, req.Label); err != nil {
		return nil, err
	}
	factor, err := s.userAuthFactorSvc.Get(ctx, req.FactorID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"factor": userFactorToQUIC(factor)})
}

func (s *Server) handleUserAuthFactorLogin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userAuthFactorSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user_authfactor"}
	}
	var req quicUserFactorLoginRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	factor, err := s.userAuthFactorSvc.LookupByCredentialKey(ctx, auth.AuthFactorType(req.Type), req.LookupKey)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"user_id": factor.UserID,
		"active":  true,
		"factor":  userFactorToQUIC(factor),
	})
}
