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

package grpc

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dekwire"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// ---------------------------------------------------------------------------
// Tenant DEK keyring handlers
//
// The six handlers are 1:1 wrappers around service.DEKServicer, which owns
// the tenant scoping. The Unix transport serves the same handlers.
// ---------------------------------------------------------------------------

// dekUnconfigured is the status for a server built without a DEK service,
// rendered through the classifier like every other service error.
func dekUnconfigured() error {
	return mapServiceError(&ServiceNotConfiguredError{Service: "dek"})
}

// TenantDEKStatus returns the caller's keyring versions.
func (s *qrdbServiceServer) TenantDEKStatus(ctx context.Context, req *pb.TenantDEKRequest) (*pb.TenantDEKStatusResponse, error) {
	if s.dekSvc == nil {
		return nil, dekUnconfigured()
	}
	st, err := s.dekSvc.Status(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return dekwire.StatusProto(st), nil
}

// TenantDEKRotate adds a key version and makes it current.
func (s *qrdbServiceServer) TenantDEKRotate(ctx context.Context, req *pb.TenantDEKRequest) (*pb.TenantDEKStatusResponse, error) {
	if s.dekSvc == nil {
		return nil, dekUnconfigured()
	}
	st, err := s.dekSvc.Rotate(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return dekwire.StatusProto(st), nil
}

// TenantDEKRetire destroys an old key version.
func (s *qrdbServiceServer) TenantDEKRetire(ctx context.Context, req *pb.TenantDEKRetireRequest) (*pb.TenantDEKStatusResponse, error) {
	if s.dekSvc == nil {
		return nil, dekUnconfigured()
	}
	st, err := s.dekSvc.Retire(ctx, req.GetTenantId(), req.GetVersion())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return dekwire.StatusProto(st), nil
}

// TenantDEKEncrypt seals plaintext under the current key version.
func (s *qrdbServiceServer) TenantDEKEncrypt(ctx context.Context, req *pb.TenantDEKEncryptRequest) (*pb.TenantDEKEncryptResponse, error) {
	if s.dekSvc == nil {
		return nil, dekUnconfigured()
	}
	sealed, err := s.dekSvc.Encrypt(ctx, req.GetTenantId(), req.GetPlaintext(), req.GetAad())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.TenantDEKEncryptResponse{Ciphertext: sealed.Ciphertext, Version: sealed.Version}, nil
}

// TenantDEKDecrypt opens a ciphertext sealed for the caller's tenant.
func (s *qrdbServiceServer) TenantDEKDecrypt(ctx context.Context, req *pb.TenantDEKDecryptRequest) (*pb.TenantDEKDecryptResponse, error) {
	if s.dekSvc == nil {
		return nil, dekUnconfigured()
	}
	opened, err := s.dekSvc.Decrypt(ctx, req.GetTenantId(), req.GetCiphertext(), req.GetAad())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.TenantDEKDecryptResponse{Plaintext: opened.Plaintext, Version: opened.Version}, nil
}

// TenantDEKRewrap re-seals a ciphertext under the current key version.
func (s *qrdbServiceServer) TenantDEKRewrap(ctx context.Context, req *pb.TenantDEKRewrapRequest) (*pb.TenantDEKRewrapResponse, error) {
	if s.dekSvc == nil {
		return nil, dekUnconfigured()
	}
	rw, err := s.dekSvc.Rewrap(ctx, req.GetTenantId(), req.GetCiphertext(), req.GetAad())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.TenantDEKRewrapResponse{
		Ciphertext:  rw.Ciphertext,
		FromVersion: rw.FromVersion,
		ToVersion:   rw.ToVersion,
	}, nil
}
