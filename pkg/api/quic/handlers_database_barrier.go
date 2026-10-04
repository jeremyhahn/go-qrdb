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

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// handleDatabaseSeal seals the encryption barrier for a database.
func (s *Server) handleDatabaseSeal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}
	var req pb.DatabaseSealRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.databaseSvc.SealDatabase(ctx, req.GetName()); err != nil {
		return nil, err
	}
	out, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleDatabaseUnseal unseals the encryption barrier for a database.
func (s *Server) handleDatabaseUnseal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}
	var req pb.DatabaseUnsealRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	creds, err := seal.CredentialsFor(ctx, []byte(req.GetSecret()))
	if err != nil {
		return nil, err
	}
	defer creds.Secret.Destroy()
	if err := s.databaseSvc.UnsealDatabase(ctx, req.GetName(), creds); err != nil {
		return nil, err
	}
	out, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleDatabaseBarrierStatus returns the encryption-barrier status for a database.
func (s *Server) handleDatabaseBarrierStatus(ctx context.Context, payload []byte) ([]byte, error) {
	if s.databaseSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "database"}
	}
	var req pb.DatabaseBarrierStatusRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	st, err := s.databaseSvc.DatabaseBarrierStatus(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	out, err := proto.Marshal(&pb.DatabaseBarrierStatusResponse{
		Sealed:            st.Sealed,
		Strategy:          string(st.Strategy),
		HardwareBacked:    st.HardwareBacked,
		InitializedAtUnix: st.InitializedAt.Unix(),
		ClusterSeal:       grpcapi.ClusterSealToProto(st.ClusterSeal),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
