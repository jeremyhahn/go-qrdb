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

package unix

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.BarrierClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// BarrierClient implementation
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the barrier with a single master key.
func (t *Transport) BarrierInitialize(ctx context.Context, req *transport.BarrierInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.BarrierInitialize(ctx, &pb.BarrierInitializeRequest{
		Secret:  req.Secret,
		SoPin:   req.SOPin,
		UserPin: req.UserPin,
	})
	if err != nil {
		return mapGRPCError("BarrierInitialize", err)
	}
	return nil
}

// BarrierUnseal unseals the barrier using the master key.
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.BarrierUnseal(ctx, &pb.BarrierUnsealRequest{
		Secret: req.Secret,
	})
	if err != nil {
		return mapGRPCError("BarrierUnseal", err)
	}
	return nil
}

// BarrierSeal seals the barrier, preventing further data access until it is
// unsealed again.
func (t *Transport) BarrierSeal(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.BarrierSeal(ctx, &emptypb.Empty{})
	if err != nil {
		return mapGRPCError("BarrierSeal", err)
	}
	return nil
}

// BarrierStatus returns the current state of the cryptographic barrier.
func (t *Transport) BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierStatus(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("BarrierStatus", err)
	}
	return &transport.BarrierStatusResponse{
		Sealed:          resp.GetSealed(),
		Strategy:        resp.GetStrategy(),
		HardwareBacked:  resp.GetHardwareBacked(),
		InitializedAt:   resp.GetInitializedAt(),
		ShamirThreshold: int(resp.GetShamirThreshold()),
		ShamirTotal:     int(resp.GetShamirTotal()),
	}, nil
}

// BarrierInitializeShamir initializes the barrier using Shamir secret sharing
// and returns the generated key shares.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierShamirInitialize(ctx, &pb.BarrierShamirInitializeRequest{
		Secret:      req.Secret,
		Threshold:   int32(req.Threshold),
		TotalShares: int32(req.TotalShares),
		SoPin:       req.SOPin,
		UserPin:     req.UserPin,
	})
	if err != nil {
		return nil, mapGRPCError("BarrierInitializeShamir", err)
	}
	return &transport.BarrierInitializeShamirResponse{
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
		ShareCount:  int(resp.GetShareCount()),
	}, nil
}

// BarrierUnsealWithShare submits a single Shamir share toward the unseal
// threshold.
func (t *Transport) BarrierUnsealWithShare(ctx context.Context, req *transport.BarrierUnsealShareRequest) (*transport.BarrierUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierShamirUnsealShare(ctx, &pb.BarrierShamirUnsealShareRequest{
		Share: req.Share,
	})
	if err != nil {
		return nil, mapGRPCError("BarrierUnsealWithShare", err)
	}
	return &transport.BarrierUnsealShareResponse{
		Required:  int(resp.GetRequired()),
		Submitted: int(resp.GetSubmitted()),
		Complete:  resp.GetComplete(),
	}, nil
}

// BarrierUnsealWithShares submits multiple Shamir shares to unseal the
// barrier in a single request.
func (t *Transport) BarrierUnsealWithShares(ctx context.Context, req *transport.BarrierUnsealSharesRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.BarrierShamirUnsealShares(ctx, &pb.BarrierShamirUnsealSharesRequest{
		Shares: req.Shares,
	})
	if err != nil {
		return mapGRPCError("BarrierUnsealWithShares", err)
	}
	return nil
}

// BarrierRekey generates new Shamir shares with the specified threshold and
// total count, replacing the existing shares.
func (t *Transport) BarrierRekey(ctx context.Context, req *transport.BarrierRekeyRequest) (*transport.BarrierRekeyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRekey(ctx, &pb.BarrierRekeyRequest{
		Threshold: int32(req.Threshold),
		Total:     int32(req.Total),
		SoPin:     req.SOPin,
	})
	if err != nil {
		return nil, mapGRPCError("BarrierRekey", err)
	}
	return &transport.BarrierRekeyResponse{
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
		ShareCount:  int(resp.GetShareCount()),
	}, nil
}

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a keyed
// strategy over the gRPC-on-UDS transport (the default CLI transport).
func (t *Transport) BarrierEnableEncryption(ctx context.Context, req *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierEnableEncryption(ctx, &pb.BarrierEnableEncryptionRequest{
		TenantId:        req.TenantID,
		Strategy:        req.Strategy,
		Mechanism:       req.Mechanism,
		Availability:    req.Availability,
		Passphrase:      req.Passphrase,
		ShamirThreshold: int32(req.ShamirThreshold),
		ShamirTotal:     int32(req.ShamirTotal),
	})
	if err != nil {
		return nil, mapGRPCError("BarrierEnableEncryption", err)
	}
	return &transport.BarrierEnableEncryptionResponse{
		Status: resp.GetStatus(),
		Scope:  resp.GetScope(),
	}, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// scenarios.
func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRecoveryGenerate(ctx, &pb.BarrierRecoveryGenerateRequest{
		Threshold: int32(req.Threshold),
		Total:     int32(req.Total),
		SoPin:     req.SOPin,
	})
	if err != nil {
		return nil, mapGRPCError("BarrierGenerateRecoveryKeys", err)
	}
	return &transport.BarrierRecoveryKeysResponse{
		Threshold:  int(resp.GetThreshold()),
		Total:      int(resp.GetTotal()),
		ShareCount: int(resp.GetShareCount()),
	}, nil
}

// BarrierRecoverWithKeys unseals the barrier using recovery keys.
func (t *Transport) BarrierRecoverWithKeys(ctx context.Context, req *transport.BarrierRecoverWithKeysRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.BarrierRecoveryRecover(ctx, &pb.BarrierRecoveryRecoverRequest{
		Keys: req.Keys,
	})
	if err != nil {
		return mapGRPCError("BarrierRecoverWithKeys", err)
	}
	return nil
}

// BarrierDeleteRecoveryKeys deletes all stored recovery keys.
func (t *Transport) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.BarrierRecoveryDelete(ctx, &pb.BarrierRecoveryDeleteRequest{})
	if err != nil {
		return mapGRPCError("BarrierDeleteRecoveryKeys", err)
	}
	return nil
}

// BarrierHasRecoveryKeys checks whether recovery keys have been generated.
func (t *Transport) BarrierHasRecoveryKeys(ctx context.Context) (*transport.BarrierHasRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRecoveryStatus(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("BarrierHasRecoveryKeys", err)
	}
	return &transport.BarrierHasRecoveryKeysResponse{
		HasKeys: resp.GetHasRecoveryKeys(),
	}, nil
}

// BarrierGenerateRootToken generates a new root token using the provided
// Shamir shares.
func (t *Transport) BarrierGenerateRootToken(ctx context.Context, req *transport.BarrierGenerateRootTokenRequest) (*transport.BarrierRootTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRootToken(ctx, &pb.BarrierRootTokenRequest{
		Shares: req.Shares,
	})
	if err != nil {
		return nil, mapGRPCError("BarrierGenerateRootToken", err)
	}
	return &transport.BarrierRootTokenResponse{
		Token:     resp.GetToken(),
		CreatedAt: resp.GetCreatedAt(),
	}, nil
}

// ---------------------------------------------------------------------------
// Security Officer / share-claim operations
// ---------------------------------------------------------------------------

// OfficerRegister registers a Security Officer for the system barrier.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierOfficerRegister(ctx, &pb.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CsrPem:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, mapGRPCError("OfficerRegister", err)
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    resp.GetCertPem(),
		ShareIndex: int(resp.GetShareIndex()),
	}, nil
}

// OfficerList lists all registered Security Officers for the system barrier.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierOfficerList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("OfficerList", err)
	}
	return unixProtoToOfficerStatusSlice(resp.GetOfficers()), nil
}

// OfficerClaimStatus returns per-officer claim status (no share data).
func (t *Transport) OfficerClaimStatus(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierOfficerClaimStatus(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("OfficerClaimStatus", err)
	}
	return unixProtoToOfficerStatusSlice(resp.GetOfficers()), nil
}

// ShareClaimBegin issues a challenge nonce to the named Security Officer.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierShareClaimBegin(ctx, &pb.BarrierShareClaimBeginRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, mapGRPCError("ShareClaimBegin", err)
	}
	expiresAt, _ := time.Parse(time.RFC3339, resp.GetExpiresAt())
	return &transport.ShareClaimBeginResponse{
		NonceHex:  resp.GetNonceHex(),
		ExpiresAt: expiresAt,
	}, nil
}

// ShareClaimComplete verifies the officer's nonce signature and returns the
// share bytes exactly once.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierShareClaimComplete(ctx, &pb.BarrierShareClaimCompleteRequest{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, mapGRPCError("ShareClaimComplete", err)
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: int(resp.GetShareIndex()),
		ShareBytes: resp.GetShareBytes(),
	}, nil
}

// ShamirSealVote submits a Shamir share as a seal vote.
func (t *Transport) ShamirSealVote(ctx context.Context, req *transport.ShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierShamirSealVote(ctx, &pb.BarrierShamirSealVoteRequest{
		Share: req.Share,
	})
	if err != nil {
		return nil, mapGRPCError("ShamirSealVote", err)
	}
	return &transport.ShamirSealVoteResponse{
		Required:  int(resp.GetRequired()),
		Submitted: int(resp.GetSubmitted()),
		Complete:  resp.GetComplete(),
	}, nil
}

// RecoveryOfficerRegister registers a Security Officer for recovery key
// distribution.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRecoveryOfficerRegister(ctx, &pb.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CsrPem:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, mapGRPCError("RecoveryOfficerRegister", err)
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    resp.GetCertPem(),
		ShareIndex: int(resp.GetShareIndex()),
	}, nil
}

// RecoveryShareClaimBegin issues a challenge nonce for a recovery officer.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRecoveryShareClaimBegin(ctx, &pb.BarrierShareClaimBeginRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, mapGRPCError("RecoveryShareClaimBegin", err)
	}
	expiresAt, _ := time.Parse(time.RFC3339, resp.GetExpiresAt())
	return &transport.ShareClaimBeginResponse{
		NonceHex:  resp.GetNonceHex(),
		ExpiresAt: expiresAt,
	}, nil
}

// RecoveryShareClaimComplete verifies the recovery officer's nonce signature
// and returns the recovery share bytes exactly once.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRecoveryShareClaimComplete(ctx, &pb.BarrierShareClaimCompleteRequest{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, mapGRPCError("RecoveryShareClaimComplete", err)
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: int(resp.GetShareIndex()),
		ShareBytes: resp.GetShareBytes(),
	}, nil
}

// unixProtoToOfficerStatusSlice converts pb.OfficerStatus slice to transport.
func unixProtoToOfficerStatusSlice(officers []*pb.OfficerStatus) []*transport.OfficerStatus {
	result := make([]*transport.OfficerStatus, 0, len(officers))
	for _, o := range officers {
		s := &transport.OfficerStatus{
			Username:       o.GetUsername(),
			CertThumbprint: o.GetCertThumbprint(),
			ShareIndex:     int(o.GetShareIndex()),
			AuthFactor:     o.GetAuthFactor(),
			SharePending:   o.GetSharePending(),
		}
		if ea := o.GetEnrolledAt(); ea != "" {
			s.EnrolledAt, _ = time.Parse(time.RFC3339, ea)
		}
		if ca := o.GetClaimedAt(); ca != "" {
			s.ClaimedAt, _ = time.Parse(time.RFC3339, ca)
		}
		result = append(result, s)
	}
	return result
}
