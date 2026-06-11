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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion for the M14 user-side surface.
var _ transport.UserAuthFactorClient = (*Transport)(nil)

// AddAuthFactor invokes UserAuthFactorService.Add via gRPC.
func (t *Transport) AddAuthFactor(ctx context.Context, req *transport.AuthFactorAddRequest) (*transport.AuthFactorAddResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).Add(ctx, &pb.UserAuthFactorAddRequest{
		UserId:         req.UserID,
		Type:           req.Type,
		Label:          req.Label,
		Credential:     req.Credential,
		SpkiCredential: req.SPKICredential,
		Metadata:       req.Metadata,
		CreatedBy:      req.CreatedBy,
	})
	if err != nil {
		return nil, mapGRPCError("AddAuthFactor", err)
	}
	return &transport.AuthFactorAddResponse{Factor: pbUserFactorToTransport(resp.GetFactor())}, nil
}

// ListAuthFactors invokes UserAuthFactorService.List via gRPC.
func (t *Transport) ListAuthFactors(ctx context.Context, req *transport.AuthFactorListRequest) (*transport.AuthFactorListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).List(ctx, &pb.UserAuthFactorListRequest{
		UserId: req.UserID,
	})
	if err != nil {
		return nil, mapGRPCError("ListAuthFactors", err)
	}
	out := transport.AuthFactorListResponse{
		Factors: make([]transport.AuthFactorRecord, len(resp.GetFactors())),
		Total:   int(resp.GetTotal()),
	}
	for i, f := range resp.GetFactors() {
		out.Factors[i] = pbUserFactorToTransport(f)
	}
	return &out, nil
}

// GetAuthFactor invokes UserAuthFactorService.Get via gRPC.
func (t *Transport) GetAuthFactor(ctx context.Context, req *transport.AuthFactorGetRequest) (*transport.AuthFactorGetResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).Get(ctx, &pb.UserAuthFactorGetRequest{
		FactorId: req.FactorID,
	})
	if err != nil {
		return nil, mapGRPCError("GetAuthFactor", err)
	}
	return &transport.AuthFactorGetResponse{Factor: pbUserFactorToTransport(resp.GetFactor())}, nil
}

// RemoveAuthFactor invokes UserAuthFactorService.Remove via gRPC.
func (t *Transport) RemoveAuthFactor(ctx context.Context, req *transport.AuthFactorRemoveRequest) (*transport.AuthFactorRemoveResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).Remove(ctx, &pb.UserAuthFactorRemoveRequest{
		FactorId: req.FactorID,
		Force:    req.Force,
	})
	if err != nil {
		return nil, mapGRPCError("RemoveAuthFactor", err)
	}
	return &transport.AuthFactorRemoveResponse{Status: resp.GetStatus()}, nil
}

// RotateAuthFactor invokes UserAuthFactorService.Rotate via gRPC.
func (t *Transport) RotateAuthFactor(ctx context.Context, req *transport.AuthFactorRotateRequest) (*transport.AuthFactorRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).Rotate(ctx, &pb.UserAuthFactorRotateRequest{
		OldFactorId: req.OldFactorID,
		NewFactor: &pb.UserAuthFactorNewFactor{
			UserId:         req.NewFactor.UserID,
			Type:           req.NewFactor.Type,
			Label:          req.NewFactor.Label,
			Credential:     req.NewFactor.Credential,
			SpkiCredential: req.NewFactor.SPKICredential,
			Metadata:       req.NewFactor.Metadata,
			CreatedBy:      req.NewFactor.CreatedBy,
		},
	})
	if err != nil {
		return nil, mapGRPCError("RotateAuthFactor", err)
	}
	return &transport.AuthFactorRotateResponse{NewFactor: pbUserFactorToTransport(resp.GetNewFactor())}, nil
}

// RelabelAuthFactor invokes UserAuthFactorService.Relabel via gRPC.
func (t *Transport) RelabelAuthFactor(ctx context.Context, req *transport.AuthFactorRelabelRequest) (*transport.AuthFactorRelabelResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).Relabel(ctx, &pb.UserAuthFactorRelabelRequest{
		FactorId: req.FactorID,
		Label:    req.Label,
	})
	if err != nil {
		return nil, mapGRPCError("RelabelAuthFactor", err)
	}
	return &transport.AuthFactorRelabelResponse{Factor: pbUserFactorToTransport(resp.GetFactor())}, nil
}

// LoginAuthFactor invokes UserAuthFactorService.Login via gRPC.
func (t *Transport) LoginAuthFactor(ctx context.Context, req *transport.AuthFactorLoginRequest) (*transport.AuthFactorLoginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := pb.NewUserAuthFactorServiceClient(t.conn).Login(ctx, &pb.UserAuthFactorLoginRequest{
		Type:      req.Type,
		LookupKey: req.LookupKey,
	})
	if err != nil {
		return nil, mapGRPCError("LoginAuthFactor", err)
	}
	return &transport.AuthFactorLoginResponse{
		UserID:   resp.GetUserId(),
		Username: resp.GetUsername(),
		Email:    resp.GetEmail(),
		TenantID: resp.GetTenantId(),
		Roles:    resp.GetRoles(),
		Active:   resp.GetActive(),
		Factor:   pbUserFactorToTransport(resp.GetFactor()),
	}, nil
}

// pbUserFactorToTransport translates a protobuf UserAuthFactorRecord into the
// SDK transport shape, parsing RFC3339 timestamps.
func pbUserFactorToTransport(r *pb.UserAuthFactorRecord) transport.AuthFactorRecord {
	if r == nil {
		return transport.AuthFactorRecord{}
	}
	out := transport.AuthFactorRecord{
		ID:             r.GetId(),
		UserID:         r.GetUserId(),
		Type:           r.GetType(),
		Label:          r.GetLabel(),
		Credential:     r.GetCredential(),
		SPKICredential: r.GetSpkiCredential(),
		Metadata:       r.GetMetadata(),
		CreatedBy:      r.GetCreatedBy(),
	}
	out.RegisteredAt, _ = time.Parse(time.RFC3339, r.GetRegisteredAt())
	if lu := r.GetLastUsedAt(); lu != "" {
		if ts, err := time.Parse(time.RFC3339, lu); err == nil {
			out.LastUsedAt = &ts
		}
	}
	if rv := r.GetRevokedAt(); rv != "" {
		if ts, err := time.Parse(time.RFC3339, rv); err == nil {
			out.RevokedAt = &ts
		}
	}
	return out
}
