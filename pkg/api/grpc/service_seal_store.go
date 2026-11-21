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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// The SealStore* RPCs serve the node's seal store. The interceptor gates
// each on seal:store_manage (the proto option), the permission the embedded
// SDK transport checks; a node with no seal store wired answers
// ServiceNotConfiguredError after that check.

func (s *qrdbServiceServer) sealStoreConfigured() error {
	if s.sealStoreSvc == nil {
		return mapServiceError(&ServiceNotConfiguredError{Service: "seal store"})
	}
	return nil
}

// SealStorePut stores a named secret in the node's seal store.
func (s *qrdbServiceServer) SealStorePut(ctx context.Context, req *pb.SealStorePutRequest) (*emptypb.Empty, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	if err := s.sealStoreSvc.Put(ctx, req.GetName(), req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// SealStoreGet returns a named secret from the node's seal store.
func (s *qrdbServiceServer) SealStoreGet(ctx context.Context, req *pb.SealStoreNameRequest) (*pb.SealStoreGetResponse, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	secret, err := s.sealStoreSvc.Get(ctx, req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.SealStoreGetResponse{Name: req.GetName(), Secret: secret}, nil
}

// SealStoreDelete deletes a named secret from the node's seal store.
func (s *qrdbServiceServer) SealStoreDelete(ctx context.Context, req *pb.SealStoreNameRequest) (*emptypb.Empty, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	if err := s.sealStoreSvc.Delete(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// SealStoreList lists the names stored in the node's seal store.
func (s *qrdbServiceServer) SealStoreList(ctx context.Context, _ *emptypb.Empty) (*pb.SealStoreListResponse, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	names, err := s.sealStoreSvc.List(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.SealStoreListResponse{Names: names}, nil
}

// SealStoreReseal reseals a named secret under the current barrier key.
func (s *qrdbServiceServer) SealStoreReseal(ctx context.Context, req *pb.SealStoreNameRequest) (*emptypb.Empty, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	if err := s.sealStoreSvc.Reseal(ctx, req.GetName()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// SealStoreStatus returns the node seal store's status.
func (s *qrdbServiceServer) SealStoreStatus(ctx context.Context, _ *emptypb.Empty) (*pb.SealStoreStatusResponse, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	st, err := s.sealStoreSvc.Status(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.SealStoreStatusResponse{
		Available:   st.Available,
		SealerId:    string(st.Strategy),
		SecretCount: int32(st.SecretCount), //nolint:gosec // a node seal store holds far fewer than 2^31 secrets
		SecretNames: st.SecretNames,
	}, nil
}
