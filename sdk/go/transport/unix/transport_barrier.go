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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"
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
	if err := t.requireTLS(req); err != nil {
		return err
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

// requireTLS refuses a request carrying a barrier secret on a socket that is
// not TLS, before the request is built, exactly as the SO PIN interceptor
// refuses the SO PIN: a Unix socket is a plaintext connection unless TLS is
// configured on it.
func (t *Transport) requireTLS(req transport.BarrierSecretCarrier) error {
	return transport.RequireTLSForBarrierSecret("unix", t.config.TLSEnabled, req)
}

// BarrierUnseal unseals the barrier using the master key.
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return err
	}
	_, err := t.client.BarrierUnseal(ctx, &pb.BarrierUnsealRequest{
		Secret: req.Secret,
	})
	if err != nil {
		return mapGRPCError("BarrierUnseal", err)
	}
	return nil
}

// BarrierChangePassphrase implements transport.BarrierClient.
func (t *Transport) BarrierChangePassphrase(ctx context.Context, req *transport.BarrierChangePassphraseRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return err
	}
	// The server verifies the SO PIN the request carries, as for BarrierSeal.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierChangePassphrase", "")
	if err != nil {
		return err
	}
	_, err = t.client.BarrierChangePassphrase(ctx, &pb.BarrierChangePassphraseRequest{
		Current: req.Current,
		Next:    req.Next,
		SoPin:   soPIN,
	})
	if err != nil {
		return mapGRPCError("BarrierChangePassphrase", err)
	}
	return nil
}

// BarrierSeal seals the barrier, preventing further data access until it is
// unsealed again.
func (t *Transport) BarrierSeal(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierSeal", "")
	if err != nil {
		return err
	}
	_, err = t.client.BarrierSeal(ctx, &pb.BarrierSealRequest{SoPin: soPIN})
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
		Sealed:           resp.GetSealed(),
		Initialized:      resp.GetInitialized(),
		Strategy:         resp.GetStrategy(),
		HardwareBacked:   resp.GetHardwareBacked(),
		InitializedAt:    resp.GetInitializedAt(),
		ShamirThreshold:  int(resp.GetShamirThreshold()),
		ShamirTotal:      int(resp.GetShamirTotal()),
		ClusterSeal:      sealconv.FromProto(resp.GetClusterSeal()),
		ClusterMigration: sealconv.MigrationFromProto(resp.GetClusterMigration()),
	}, nil
}

// BarrierClusterUnseal clears a cluster-wide seal of the node barrier and
// returns its cluster seal state. It unseals nothing.
func (t *Transport) BarrierClusterUnseal(ctx context.Context) (*transport.ClusterSeal, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierClusterUnseal(ctx, &pb.BarrierClusterUnsealRequest{})
	if err != nil {
		return nil, mapGRPCError("BarrierClusterUnseal", err)
	}
	return sealconv.FromProto(resp), nil
}

// BarrierInitializeShamir initializes the barrier under Shamir custody and
// distributes one sealed envelope to each enrolled unseal-key holder.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return nil, err
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierInitializeShamir", req.SOPin)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.BarrierShamirInitialize(ctx, &pb.BarrierShamirInitializeRequest{
		Threshold:   int32(req.Threshold),   //nolint:gosec
		TotalShares: int32(req.TotalShares), //nolint:gosec
		Policy:      req.Policy,
		SoPin:       soPIN,
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

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a keyed
// strategy over the gRPC-on-UDS transport (the default CLI transport).
func (t *Transport) BarrierEnableEncryption(ctx context.Context, req *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return nil, err
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
	if err := t.requireTLS(req); err != nil {
		return nil, err
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierGenerateRecoveryKeys", req.SOPin)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.BarrierRecoveryGenerate(ctx, &pb.BarrierRecoveryGenerateRequest{
		Threshold: int32(req.Threshold), //nolint:gosec
		Policy:    req.Policy,
		SoPin:     soPIN,
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

// ---------------------------------------------------------------------------
// Share holders (Security Officers and key custodians)
// ---------------------------------------------------------------------------

// OfficerRegister enrols a share holder of the unseal key.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	in, err := holderconv.RegisterToPB(req)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.BarrierOfficerRegister(ctx, in)
	if err != nil {
		return nil, mapGRPCError("OfficerRegister", err)
	}
	return holderconv.RegisterFromPB(resp), nil
}

// RecoveryOfficerRegister enrols a holder of the recovery key.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	in, err := holderconv.RegisterToPB(req)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.BarrierRecoveryOfficerRegister(ctx, in)
	if err != nil {
		return nil, mapGRPCError("RecoveryOfficerRegister", err)
	}
	return holderconv.RegisterFromPB(resp), nil
}

// OfficerList lists the unseal key's holders and their claim state.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierOfficerList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("OfficerList", err)
	}
	return holderconv.StatusesFromPB(resp.GetOfficers()), nil
}

// ShareClaimBegin issues a claim nonce to an unseal-key holder.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierShareClaimBegin(ctx, &pb.BarrierShareClaimBeginRequest{Username: req.Username})
	if err != nil {
		return nil, mapGRPCError("ShareClaimBegin", err)
	}
	return holderconv.ClaimBeginFromPB(resp.GetNonceHex(), resp.GetExpiresAt()), nil
}

// ShareClaimComplete delivers an unseal-key holder's sealed envelope once.
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
	return &transport.ShareClaimCompleteResponse{ShareIndex: int(resp.GetShareIndex()), Envelope: resp.GetEnvelope()}, nil
}

// RecoveryShareClaimBegin issues a claim nonce to a recovery holder.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierRecoveryShareClaimBegin(ctx, &pb.BarrierShareClaimBeginRequest{Username: req.Username})
	if err != nil {
		return nil, mapGRPCError("RecoveryShareClaimBegin", err)
	}
	return holderconv.ClaimBeginFromPB(resp.GetNonceHex(), resp.GetExpiresAt()), nil
}

// RecoveryShareClaimComplete delivers a recovery holder's envelope once.
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
	return &transport.ShareClaimCompleteResponse{ShareIndex: int(resp.GetShareIndex()), Envelope: resp.GetEnvelope()}, nil
}

// BarrierQuorumBegin issues the factor challenge a holder answers.
func (t *Transport) BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.BarrierQuorumBegin(ctx, holderconv.BeginToPB(req))
	if err != nil {
		return nil, mapGRPCError("BarrierQuorumBegin", err)
	}
	return holderconv.ChallengeFromPB(resp), nil
}

// BarrierQuorumSubmit submits one authenticated holder's share.
func (t *Transport) BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return nil, err
	}
	resp, err := t.client.BarrierQuorumSubmit(ctx, holderconv.SubmitToPB(req))
	if err != nil {
		return nil, mapGRPCError("BarrierQuorumSubmit", err)
	}
	return holderconv.OutcomeFromPB(resp), nil
}

// BarrierRekeyStart opens a rekey round.
func (t *Transport) BarrierRekeyStart(ctx context.Context, req *transport.RekeyStartRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return nil, err
	}
	// The SO PIN travels in the request's own so_pin field; it comes from the
	// PIN bound by transport.WithSOPIN when the request carries none.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierRekeyStart", req.SOPin)
	if err != nil {
		return nil, err
	}
	resolved := *req
	resolved.SOPin = soPIN
	resp, err := t.client.BarrierRekeyStart(ctx, holderconv.RekeyToPB(&resolved))
	if err != nil {
		return nil, mapGRPCError("BarrierRekeyStart", err)
	}
	return holderconv.RoundFromPB(resp), nil
}

// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey.
func (t *Transport) BarrierOfficerRevoke(ctx context.Context, req *transport.OfficerRevokeRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if err := t.requireTLS(req); err != nil {
		return nil, err
	}
	// The SO PIN travels in the request's own so_pin field; it comes from the
	// PIN bound by transport.WithSOPIN when the request carries none.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierOfficerRevoke", req.SOPin)
	if err != nil {
		return nil, err
	}
	resolved := *req
	resolved.SOPin = soPIN
	resp, err := t.client.BarrierOfficerRevoke(ctx, holderconv.RevokeToPB(&resolved))
	if err != nil {
		return nil, mapGRPCError("BarrierOfficerRevoke", err)
	}
	return holderconv.RoundFromPB(resp), nil
}
