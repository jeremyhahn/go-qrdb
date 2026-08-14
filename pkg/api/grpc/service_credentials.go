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
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
)

// ---------------------------------------------------------------------------
// Credential handlers
//
// All eight handlers are 1:1 wrappers around service.CredentialServicer.
// They validate that the credential service was wired into the server,
// translate proto request types into service inputs, and rely on the shared
// mapCredentialError (declared in credentials_server.go) to translate
// service-layer errors back into appropriate gRPC status codes.
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value through the configured
// credential management strategy.
func (s *qrdbServiceServer) CredentialSubmit(ctx context.Context, req *pb.CredentialSubmitRequest) (*emptypb.Empty, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	if err := s.credentialSvc.CredentialSubmit(ctx, req.GetName(), req.GetValue()); err != nil {
		return nil, mapCredentialError(err)
	}
	return &emptypb.Empty{}, nil
}

// CredentialStrategy returns the active credential management strategy name.
func (s *qrdbServiceServer) CredentialStrategy(ctx context.Context, _ *emptypb.Empty) (*pb.CredentialStrategyResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	return &pb.CredentialStrategyResponse{
		Strategy: s.credentialSvc.CredentialStrategy(),
	}, nil
}

// CredentialDelete removes a named credential and all of its non-current
// historical versions.
func (s *qrdbServiceServer) CredentialDelete(ctx context.Context, req *pb.CredentialDeleteRequest) (*emptypb.Empty, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	if err := s.credentialSvc.CredentialDelete(ctx, req.GetName()); err != nil {
		return nil, mapCredentialError(err)
	}
	return &emptypb.Empty{}, nil
}

// CredentialList returns the sorted names of every stored credential. Values
// are never returned by enumeration.
func (s *qrdbServiceServer) CredentialList(ctx context.Context, _ *pb.CredentialListRequest) (*pb.CredentialListResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	names, err := s.credentialSvc.CredentialList(ctx)
	if err != nil {
		return nil, mapCredentialError(err)
	}
	return &pb.CredentialListResponse{
		Names: names,
	}, nil
}

// CredentialRotate atomically replaces the current value of a credential.
// When grace_period_seconds > 0 the previous version remains retrievable by
// version number until the grace period elapses.
func (s *qrdbServiceServer) CredentialRotate(ctx context.Context, req *pb.CredentialRotateRequest) (*pb.CredentialRotateResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	result, err := s.credentialSvc.CredentialRotate(ctx, req.GetName(), credentials.RotateOptions{
		NewValue:    req.GetNewValue(),
		GracePeriod: time.Duration(req.GetGracePeriodSeconds()) * time.Second,
	})
	if err != nil {
		return nil, mapCredentialError(err)
	}
	var graceUntilUnix int64
	if !result.GraceUntil.IsZero() {
		graceUntilUnix = result.GraceUntil.Unix()
	}
	return &pb.CredentialRotateResponse{
		Name:            result.Name,
		PreviousVersion: result.PreviousVersion,
		NewVersion:      result.NewVersion,
		GraceUntilUnix:  graceUntilUnix,
	}, nil
}

// CredentialRetrieveVersion returns the stored credential value for a
// specific version. version == 0 selects the current version.
func (s *qrdbServiceServer) CredentialRetrieveVersion(ctx context.Context, req *pb.CredentialRetrieveVersionRequest) (*pb.CredentialRetrieveVersionResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	value, err := s.credentialSvc.CredentialRetrieveVersion(ctx, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, mapCredentialError(err)
	}
	return &pb.CredentialRetrieveVersionResponse{
		Value: value,
	}, nil
}

// CredentialListVersions returns the version history (current + all
// non-expired grace versions) for a credential. Values are never returned.
func (s *qrdbServiceServer) CredentialListVersions(ctx context.Context, req *pb.CredentialListVersionsRequest) (*pb.CredentialListVersionsResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	versions, err := s.credentialSvc.CredentialListVersions(ctx, req.GetName())
	if err != nil {
		return nil, mapCredentialError(err)
	}
	pbVersions := make([]*pb.CredentialVersionInfo, 0, len(versions))
	for _, v := range versions {
		var createdAtUnix int64
		if !v.CreatedAt.IsZero() {
			createdAtUnix = v.CreatedAt.Unix()
		}
		var graceUntilUnix int64
		if !v.GraceUntil.IsZero() {
			graceUntilUnix = v.GraceUntil.Unix()
		}
		pbVersions = append(pbVersions, &pb.CredentialVersionInfo{
			Version:        v.Version,
			IsCurrent:      v.IsCurrent,
			CreatedAtUnix:  createdAtUnix,
			GraceUntilUnix: graceUntilUnix,
		})
	}
	return &pb.CredentialListVersionsResponse{
		Name:     req.GetName(),
		Versions: pbVersions,
	}, nil
}

// CredentialGCExpired reaps any historical versions whose grace period has
// elapsed and returns the count reaped.
func (s *qrdbServiceServer) CredentialGCExpired(ctx context.Context, req *pb.CredentialGCRequest) (*pb.CredentialGCResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	reaped, err := s.credentialSvc.CredentialGCExpired(ctx, req.GetName())
	if err != nil {
		return nil, mapCredentialError(err)
	}
	return &pb.CredentialGCResponse{
		Reaped: int32(reaped),
	}, nil
}

// CredentialStore persists a credential value under the given name.
func (s *qrdbServiceServer) CredentialStore(ctx context.Context, req *pb.CredentialStoreRequest) (*emptypb.Empty, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	if err := s.credentialSvc.CredentialStore(ctx, req.GetName(), req.GetValue()); err != nil {
		return nil, mapCredentialError(err)
	}
	return &emptypb.Empty{}, nil
}

// CredentialRetrieve returns the credential value for the given name.
func (s *qrdbServiceServer) CredentialRetrieve(ctx context.Context, req *pb.CredentialRetrieveRequest) (*pb.CredentialRetrieveResponse, error) {
	if s.credentialSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "credential"}).Error())
	}
	value, err := s.credentialSvc.CredentialRetrieve(ctx, req.GetName())
	if err != nil {
		return nil, mapCredentialError(err)
	}
	return &pb.CredentialRetrieveResponse{Value: value}, nil
}
