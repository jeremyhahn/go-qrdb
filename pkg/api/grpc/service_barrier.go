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
)

// ---------------------------------------------------------------------------
// Barrier handlers
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the storage barrier with a secret credential.
// When the request supplies a non-empty SoPin / UserPin and the server has a
// pin.PINBackend configured, the PINs are persisted on the backend before
// the barrier is initialized. With no backend the PIN fields are ignored.
func (s *qrdbServiceServer) BarrierInitialize(ctx context.Context, req *pb.BarrierInitializeRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := applyBarrierInitializePINs(s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, mapPINError(err)
	}

	if err := s.barrierSvc.BarrierInitialize(ctx, req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierUnseal unseals the storage barrier with a secret credential.
func (s *qrdbServiceServer) BarrierUnseal(ctx context.Context, req *pb.BarrierUnsealRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierUnseal(ctx, req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierSeal transitions the storage barrier to the sealed state. The
// proto RPC accepts Empty so this transport does not surface SO PIN gating
// at the wire (it would require a breaking proto change). The same SO PIN
// enforcement is available through the REST surface which carries the
// barrierSealRequest JSON body; QUIC, MCP and Unix mirror REST semantics
// for parity.
func (s *qrdbServiceServer) BarrierSeal(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierSeal(ctx); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierStatus returns the current barrier status including sealed state,
// strategy, hardware backing, and initialization timestamp.
func (s *qrdbServiceServer) BarrierStatus(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierStatusResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	info, err := s.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierStatusResponse{
		Sealed:         info.Sealed,
		Strategy:       info.Strategy,
		HardwareBacked: info.HardwareBacked,
		InitializedAt:  info.InitializedAt.Format(time.RFC3339Nano),
	}, nil
}

// BarrierShamirInitialize initializes the barrier with Shamir secret sharing,
// splitting the root key into threshold-of-totalShares shares. When the
// request supplies SoPin / UserPin and a pin.PINBackend is configured,
// the PINs are persisted on the backend before initialization.
func (s *qrdbServiceServer) BarrierShamirInitialize(ctx context.Context, req *pb.BarrierShamirInitializeRequest) (*pb.BarrierShamirInitializeResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := applyBarrierInitializePINs(s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, mapPINError(err)
	}

	info, err := s.barrierSvc.BarrierInitializeShamir(ctx, req.GetSecret(), int(req.GetThreshold()), int(req.GetTotalShares()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierShamirInitializeResponse{
		Shares:      info.Shares,
		Threshold:   int32(info.Threshold),
		TotalShares: int32(info.TotalShares),
	}, nil
}

// BarrierShamirUnsealShare submits a single Shamir share toward the quorum
// threshold and reports current unseal progress.
func (s *qrdbServiceServer) BarrierShamirUnsealShare(ctx context.Context, req *pb.BarrierShamirUnsealShareRequest) (*pb.BarrierShamirUnsealShareResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	progress, err := s.barrierSvc.BarrierUnsealWithShare(ctx, req.GetShare())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierShamirUnsealShareResponse{
		Required:  int32(progress.Required),
		Submitted: int32(progress.Submitted),
		Complete:  progress.Complete,
	}, nil
}

// BarrierShamirUnsealShares unseals the barrier using a complete set of
// Shamir shares in a single call.
func (s *qrdbServiceServer) BarrierShamirUnsealShares(ctx context.Context, req *pb.BarrierShamirUnsealSharesRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierUnsealWithShares(ctx, req.GetShares()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRekey generates new Shamir shares for the existing root key with
// the specified threshold and total parameters. SO PIN verification is
// enforced when a pin.PINBackend is configured AND a SO PIN has been set.
func (s *qrdbServiceServer) BarrierRekey(ctx context.Context, req *pb.BarrierRekeyRequest) (*pb.BarrierShamirInitializeResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, mapPINError(err)
	}

	info, err := s.barrierSvc.BarrierRekey(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierShamirInitializeResponse{
		Shares:      info.Shares,
		Threshold:   int32(info.Threshold),
		TotalShares: int32(info.TotalShares),
	}, nil
}

// BarrierRecoveryGenerate generates a set of Shamir recovery shares from the
// root key for disaster recovery. SO PIN verification is enforced when a
// pin.PINBackend is configured AND a SO PIN has been set.
func (s *qrdbServiceServer) BarrierRecoveryGenerate(ctx context.Context, req *pb.BarrierRecoveryGenerateRequest) (*pb.BarrierRecoveryKeysResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, mapPINError(err)
	}

	info, err := s.barrierSvc.BarrierGenerateRecoveryKeys(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRecoveryKeysResponse{
		Keys:      info.Keys,
		Threshold: int32(info.Threshold),
		Total:     int32(info.Total),
	}, nil
}

// BarrierRecoveryRecover unseals the barrier using recovery shares that were
// previously generated by BarrierRecoveryGenerate.
func (s *qrdbServiceServer) BarrierRecoveryRecover(ctx context.Context, req *pb.BarrierRecoveryRecoverRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierRecoverWithKeys(ctx, req.GetKeys()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRecoveryDelete removes recovery key metadata from storage.
func (s *qrdbServiceServer) BarrierRecoveryDelete(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(ctx); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRecoveryStatus reports whether recovery key metadata exists in storage.
func (s *qrdbServiceServer) BarrierRecoveryStatus(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierRecoveryStatusResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	has, err := s.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRecoveryStatusResponse{
		HasRecoveryKeys: has,
	}, nil
}

// BarrierRootToken creates a new root token by proving knowledge of the
// master key through Shamir shares.
func (s *qrdbServiceServer) BarrierRootToken(ctx context.Context, req *pb.BarrierRootTokenRequest) (*pb.BarrierRootTokenResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	info, err := s.barrierSvc.BarrierGenerateRootToken(ctx, req.GetShares())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRootTokenResponse{
		Token:     info.Token,
		CreatedAt: info.CreatedAt.Format(time.RFC3339Nano),
	}, nil
}
