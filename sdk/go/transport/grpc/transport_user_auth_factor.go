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

package grpc

import (
	"context"
	"time"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion for the M14 user-side surface.
var _ transport.UserAuthFactorClient = (*Transport)(nil)

// AddAuthFactor invokes UserAuthFactorService.Add via gRPC.
func (t *Transport) AddAuthFactor(ctx context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorAddUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorAddRequest{
		UserID:     req.UserID,
		Type:       req.Type,
		Label:      req.Label,
		Credential: req.Credential,
		Metadata:   req.Metadata,
		CreatedBy:  req.CreatedBy,
	})
	if err != nil {
		return nil, mapGRPCError("AddAuthFactor", err)
	}
	return &transport.AuthFactorAddResponse{Factor: userFactorWireToTransport(&resp.Factor)}, nil
}

// ListAuthFactors invokes UserAuthFactorService.List via gRPC.
func (t *Transport) ListAuthFactors(ctx context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorListUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorListRequest{
		UserID: req.UserID,
	})
	if err != nil {
		return nil, mapGRPCError("ListAuthFactors", err)
	}
	out := transport.AuthFactorListResponse{
		Factors: make([]transport.AuthFactorRecord, len(resp.Factors)),
		Total:   resp.Total,
	}
	for i := range resp.Factors {
		out.Factors[i] = userFactorWireToTransport(&resp.Factors[i])
	}
	return &out, nil
}

// GetAuthFactor invokes UserAuthFactorService.Get via gRPC.
func (t *Transport) GetAuthFactor(ctx context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorGetUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorGetRequest{
		FactorID: req.FactorID,
	})
	if err != nil {
		return nil, mapGRPCError("GetAuthFactor", err)
	}
	return &transport.AuthFactorGetResponse{Factor: userFactorWireToTransport(&resp.Factor)}, nil
}

// RemoveAuthFactor invokes UserAuthFactorService.Remove via gRPC.
func (t *Transport) RemoveAuthFactor(ctx context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorRemoveUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorRemoveRequest{
		FactorID: req.FactorID,
		Force:    req.Force,
	})
	if err != nil {
		return nil, mapGRPCError("RemoveAuthFactor", err)
	}
	return &transport.AuthFactorRemoveResponse{Status: resp.Status}, nil
}

// RotateAuthFactor invokes UserAuthFactorService.Rotate via gRPC.
func (t *Transport) RotateAuthFactor(ctx context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorRotateUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorRotateRequest{
		OldFactorID: req.OldFactorID,
		NewFactor: grpcpkg.GRPCUserAuthFactorAddRequest{
			UserID:     req.NewFactor.UserID,
			Type:       req.NewFactor.Type,
			Label:      req.NewFactor.Label,
			Credential: req.NewFactor.Credential,
			Metadata:   req.NewFactor.Metadata,
			CreatedBy:  req.NewFactor.CreatedBy,
		},
	})
	if err != nil {
		return nil, mapGRPCError("RotateAuthFactor", err)
	}
	return &transport.AuthFactorRotateResponse{NewFactor: userFactorWireToTransport(&resp.NewFactor)}, nil
}

// RelabelAuthFactor invokes UserAuthFactorService.Relabel via gRPC.
func (t *Transport) RelabelAuthFactor(ctx context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorRelabelUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorRelabelRequest{
		FactorID: req.FactorID,
		Label:    req.Label,
	})
	if err != nil {
		return nil, mapGRPCError("RelabelAuthFactor", err)
	}
	return &transport.AuthFactorRelabelResponse{Factor: userFactorWireToTransport(&resp.Factor)}, nil
}

// LoginAuthFactor invokes UserAuthFactorService.Login via gRPC.
func (t *Transport) LoginAuthFactor(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := grpcpkg.UserAuthFactorLoginUnary(ctx, t.conn, &grpcpkg.GRPCUserAuthFactorLoginRequest{
		Type:      req.Type,
		LookupKey: req.LookupKey,
	})
	if err != nil {
		return nil, mapGRPCError("LoginAuthFactor", err)
	}
	return &transport.AuthFactorLoginResponse{
		UserID:   resp.UserID,
		Username: resp.Username,
		Email:    resp.Email,
		TenantID: resp.TenantID,
		Roles:    resp.Roles,
		Active:   resp.Active,
		Factor:   userFactorWireToTransport(&resp.Factor),
	}, nil
}

// userFactorWireToTransport translates a gRPC wire record into the SDK
// transport shape, parsing RFC3339 timestamps.
func userFactorWireToTransport(r *grpcpkg.GRPCUserAuthFactorRecord) transport.AuthFactorRecord {
	out := transport.AuthFactorRecord{
		ID:         r.ID,
		UserID:     r.UserID,
		Type:       r.Type,
		Label:      r.Label,
		Credential: r.Credential,
		Metadata:   r.Metadata,
		CreatedBy:  r.CreatedBy,
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
