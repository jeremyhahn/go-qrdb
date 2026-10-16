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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleCredentialSubmit submits a named credential value by delegating to
// the credential service layer. The credential's bytes are taken verbatim
// from the request's value field.
func (s *Server) handleCredentialSubmit(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialSubmitRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.credentialSvc.CredentialSubmit(ctx, req.GetName(), req.GetValue()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleCredentialStrategy returns the active credential management
// strategy name as reported by the credential service.
func (s *Server) handleCredentialStrategy(_ context.Context, _ []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	strategy := s.credentialSvc.CredentialStrategy()

	resp, err := proto.Marshal(&pb.CredentialStrategyResponse{
		Strategy: strategy,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCredentialDelete removes a named credential (and any non-current
// historical versions) through the credential service layer.
func (s *Server) handleCredentialDelete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialDeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.credentialSvc.CredentialDelete(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleCredentialList returns the sorted set of stored credential names.
// Values are never enumerated.
func (s *Server) handleCredentialList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	// CredentialListRequest is currently empty but is still unmarshaled
	// so unknown fields are tolerated and future filter additions remain
	// wire-compatible.
	var req pb.CredentialListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	names, err := s.credentialSvc.CredentialList(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CredentialListResponse{
		Names: names,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCredentialRotate atomically rotates a credential to a new value,
// optionally preserving the previous version for a grace period.
func (s *Server) handleCredentialRotate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialRotateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.credentialSvc.CredentialRotate(ctx, req.GetName(), credentials.RotateOptions{
		NewValue:    req.GetNewValue(),
		GracePeriod: time.Duration(req.GetGracePeriodSeconds()) * time.Second,
	})
	if err != nil {
		return nil, err
	}

	wire := &pb.CredentialRotateResponse{
		Name:            result.Name,
		PreviousVersion: result.PreviousVersion,
		NewVersion:      result.NewVersion,
	}
	if !result.GraceUntil.IsZero() {
		wire.GraceUntilUnix = result.GraceUntil.Unix()
	}

	resp, err := proto.Marshal(wire)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCredentialRetrieveVersion returns the stored bytes for a specific
// credential version. Version == 0 is treated as "current".
func (s *Server) handleCredentialRetrieveVersion(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialRetrieveVersionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	buf, err := s.credentialSvc.CredentialRetrieveVersionBuffer(ctx, req.GetName(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	// The response message is the wire DTO: the one boundary where the value
	// leaves protected memory. It is wiped as soon as it has been marshalled.
	value, err := service.CredentialWireValue(buf)
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(value)

	resp, err := proto.Marshal(&pb.CredentialRetrieveVersionResponse{
		Value: value,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCredentialListVersions returns the version history (current +
// non-expired grace versions) for a credential. Value bytes are never
// returned.
func (s *Server) handleCredentialListVersions(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialListVersionsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	versions, err := s.credentialSvc.CredentialListVersions(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	out := make([]*pb.CredentialVersionInfo, 0, len(versions))
	for _, v := range versions {
		entry := &pb.CredentialVersionInfo{
			Version:   v.Version,
			IsCurrent: v.IsCurrent,
		}
		if !v.CreatedAt.IsZero() {
			entry.CreatedAtUnix = v.CreatedAt.Unix()
		}
		if !v.GraceUntil.IsZero() {
			entry.GraceUntilUnix = v.GraceUntil.Unix()
		}
		out = append(out, entry)
	}

	resp, err := proto.Marshal(&pb.CredentialListVersionsResponse{
		Name:     req.GetName(),
		Versions: out,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCredentialGCExpired reaps historical credential versions whose
// grace period has elapsed. Returns the number of versions reaped.
func (s *Server) handleCredentialGCExpired(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialGCRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	reaped, err := s.credentialSvc.CredentialGCExpired(ctx, req.GetName())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CredentialGCResponse{
		Reaped: int32(reaped),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCredentialStore persists a credential value under the given name.
func (s *Server) handleCredentialStore(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialStoreRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.credentialSvc.CredentialStore(ctx, req.GetName(), req.GetValue()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleCredentialRetrieve returns the credential value for the given name.
func (s *Server) handleCredentialRetrieve(ctx context.Context, payload []byte) ([]byte, error) {
	if s.credentialSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "credential"}
	}

	var req pb.CredentialRetrieveRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	buf, err := s.credentialSvc.CredentialRetrieveBuffer(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	// The response message is the wire DTO: the one boundary where the value
	// leaves protected memory. It is wiped as soon as it has been marshalled.
	value, err := service.CredentialWireValue(buf)
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(value)

	out, err := proto.Marshal(&pb.CredentialRetrieveResponse{Value: value})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}
