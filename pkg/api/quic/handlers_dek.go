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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dekwire"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// Tenant DEK keyring handlers. Frames carry the gRPC protobuf messages; the
// tenant scoping is service.DEKServicer's.

// handleDEKStatus serves "dek.Status".
func (s *Server) handleDEKStatus(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantDEKRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	st, err := s.dekSvc.Status(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return marshalDEK(dekwire.StatusProto(st))
}

// handleDEKRotate serves "dek.Rotate".
func (s *Server) handleDEKRotate(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantDEKRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	st, err := s.dekSvc.Rotate(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return marshalDEK(dekwire.StatusProto(st))
}

// handleDEKRetire serves "dek.Retire".
func (s *Server) handleDEKRetire(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantDEKRetireRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	st, err := s.dekSvc.Retire(ctx, req.GetTenantId(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	return marshalDEK(dekwire.StatusProto(st))
}

// handleDEKEncrypt serves "dek.Encrypt".
func (s *Server) handleDEKEncrypt(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantDEKEncryptRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	sealed, err := s.dekSvc.Encrypt(ctx, req.GetTenantId(), req.GetPlaintext(), req.GetAad())
	if err != nil {
		return nil, err
	}
	return marshalDEK(&pb.TenantDEKEncryptResponse{Ciphertext: sealed.Ciphertext, Version: sealed.Version})
}

// handleDEKDecrypt serves "dek.Decrypt".
func (s *Server) handleDEKDecrypt(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantDEKDecryptRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	opened, err := s.dekSvc.Decrypt(ctx, req.GetTenantId(), req.GetCiphertext(), req.GetAad())
	if err != nil {
		return nil, err
	}
	return marshalDEK(&pb.TenantDEKDecryptResponse{Plaintext: opened.Plaintext, Version: opened.Version})
}

// handleDEKRewrap serves "dek.Rewrap".
func (s *Server) handleDEKRewrap(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantDEKRewrapRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	rw, err := s.dekSvc.Rewrap(ctx, req.GetTenantId(), req.GetCiphertext(), req.GetAad())
	if err != nil {
		return nil, err
	}
	return marshalDEK(&pb.TenantDEKRewrapResponse{
		Ciphertext:  rw.Ciphertext,
		FromVersion: rw.FromVersion,
		ToVersion:   rw.ToVersion,
	})
}

// marshalDEK encodes a keyring response frame.
func marshalDEK(m proto.Message) ([]byte, error) {
	resp, err := proto.Marshal(m)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
