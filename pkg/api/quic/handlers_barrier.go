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
)

// handleBarrierInitialize initializes the storage barrier with a secret
// credential by delegating to the barrier service layer.
func (s *Server) handleBarrierInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierInitialize(ctx, req.GetSecret()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierUnseal unseals the storage barrier with a secret credential
// by delegating to the barrier service layer.
func (s *Server) handleBarrierUnseal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierUnsealRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierUnseal(ctx, req.GetSecret()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierSeal transitions the storage barrier to the sealed state
// by delegating to the barrier service layer.
func (s *Server) handleBarrierSeal(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	if err := s.barrierSvc.BarrierSeal(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierStatus returns the current barrier status including seal
// state, strategy, hardware-backed flag, and initialization timestamp.
func (s *Server) handleBarrierStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	info, err := s.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierStatusResponse{
		Sealed:         info.Sealed,
		Strategy:       info.Strategy,
		HardwareBacked: info.HardwareBacked,
		InitializedAt:  info.InitializedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirInitialize initializes the barrier with Shamir secret
// sharing, splitting the root key into threshold-of-totalShares shares.
func (s *Server) handleBarrierShamirInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.barrierSvc.BarrierInitializeShamir(ctx, req.GetSecret(), int(req.GetThreshold()), int(req.GetTotalShares()))
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShamirInitializeResponse{
		Shares:      info.Shares,
		Threshold:   int32(info.Threshold),
		TotalShares: int32(info.TotalShares),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirUnsealShare submits a single Shamir share toward the
// quorum threshold and returns progress information.
func (s *Server) handleBarrierShamirUnsealShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirUnsealShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	progress, err := s.barrierSvc.BarrierUnsealWithShare(ctx, req.GetShare())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShamirUnsealShareResponse{
		Required:  int32(progress.Required),
		Submitted: int32(progress.Submitted),
		Complete:  progress.Complete,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirUnsealShares unseals the barrier using a complete set
// of Shamir shares in a single call.
func (s *Server) handleBarrierShamirUnsealShares(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirUnsealSharesRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierUnsealWithShares(ctx, req.GetShares()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierShamirListShares returns information about persisted Shamir
// shares including count, threshold, and total.
func (s *Server) handleBarrierShamirListShares(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	info, err := s.barrierSvc.BarrierShamirListShares(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShamirSharesResponse{
		Count:     int32(info.Count),
		Threshold: int32(info.Threshold),
		Total:     int32(info.Total),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirDeleteShare removes a single Shamir share by its
// 1-based index.
func (s *Server) handleBarrierShamirDeleteShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirDeleteShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierShamirDeleteShare(ctx, int(req.GetIndex())); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierShamirDeleteAllShares removes all persisted Shamir shares.
func (s *Server) handleBarrierShamirDeleteAllShares(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	if err := s.barrierSvc.BarrierShamirDeleteAllShares(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierShamirVerify validates the consistency of all persisted
// Shamir shares.
func (s *Server) handleBarrierShamirVerify(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	if err := s.barrierSvc.BarrierShamirVerify(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierRekey generates new Shamir shares for the existing root key
// with the specified threshold and total parameters.
func (s *Server) handleBarrierRekey(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRekeyRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.barrierSvc.BarrierRekey(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShamirInitializeResponse{
		Shares:      info.Shares,
		Threshold:   int32(info.Threshold),
		TotalShares: int32(info.TotalShares),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryGenerate generates a set of Shamir recovery shares
// from the root key for disaster recovery.
func (s *Server) handleBarrierRecoveryGenerate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRecoveryGenerateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.barrierSvc.BarrierGenerateRecoveryKeys(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierRecoveryKeysResponse{
		Keys:      info.Keys,
		Threshold: int32(info.Threshold),
		Total:     int32(info.Total),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryRecover unseals the barrier using recovery shares
// that were previously generated by BarrierGenerateRecoveryKeys.
func (s *Server) handleBarrierRecoveryRecover(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRecoveryRecoverRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierRecoverWithKeys(ctx, req.GetKeys()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierRecoveryDelete removes recovery key metadata from storage.
func (s *Server) handleBarrierRecoveryDelete(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierRecoveryStatus reports whether recovery key metadata exists
// in storage.
func (s *Server) handleBarrierRecoveryStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	has, err := s.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierRecoveryStatusResponse{
		HasRecoveryKeys: has,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRootToken creates a new root token by proving knowledge of
// the master key through Shamir shares.
func (s *Server) handleBarrierRootToken(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRootTokenRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.barrierSvc.BarrierGenerateRootToken(ctx, req.GetShares())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierRootTokenResponse{
		Token:     info.Token,
		CreatedAt: info.CreatedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
