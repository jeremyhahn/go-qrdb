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
	payload, err := proto.Marshal(&pb.BarrierInitializeRequest{
		Secret:  req.Secret,
		SoPin:   req.SOPin,
		UserPin: req.UserPin,
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "barrier.Initialize", payload)
	return err
}

// BarrierUnseal unseals the barrier using the master key.
func (t *Transport) BarrierUnseal(ctx context.Context, req *transport.BarrierUnsealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierUnsealRequest{
		Secret: req.Secret,
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "barrier.Unseal", payload)
	return err
}

// BarrierSeal seals the barrier, preventing further data access until it is
// unsealed again.
func (t *Transport) BarrierSeal(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	// The barrier SO PIN, when one is configured, comes from the PIN bound by
	// transport.WithSOPIN: BarrierSeal takes no request of its own.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierSeal", "")
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(&pb.BarrierSealRequest{SoPin: soPIN})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.callWithSecret(ctx, "barrier.Seal", payload)
	return err
}

// BarrierStatus returns the current state of the cryptographic barrier.
func (t *Transport) BarrierStatus(ctx context.Context) (*transport.BarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "barrier.Status", nil)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierStatusResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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
	respBytes, err := t.call(ctx, "barrier.ClusterUnseal", nil)
	if err != nil {
		return nil, err
	}
	var resp pb.ClusterSealState
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return sealconv.FromProto(&resp), nil
}

// BarrierInitializeShamir initializes the barrier under Shamir custody and
// distributes one sealed envelope to each enrolled unseal-key holder.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// QUIC carries the SO PIN in the request message; see transport.WithSOPIN.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierInitializeShamir", req.SOPin)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&pb.BarrierShamirInitializeRequest{
		Threshold:   int32(req.Threshold),   //nolint:gosec
		TotalShares: int32(req.TotalShares), //nolint:gosec
		Policy:      req.Policy,
		SoPin:       soPIN,
		UserPin:     req.UserPin,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.callWithSecret(ctx, "barrier.ShamirInitialize", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirOperationResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierInitializeShamirResponse{
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
		ShareCount:  int(resp.GetShareCount()),
	}, nil
}

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a
// keyed strategy. Maps to the barrier.EnableEncryption dispatch verb, which
// (pkg/api/quic/handlers_barrier.go) has always had a full server-side
// implementation mirroring REST/gRPC; only this client stub forwarded
// nothing (go-qrdb#558).
func (t *Transport) BarrierEnableEncryption(ctx context.Context, req *transport.BarrierEnableEncryptionRequest) (*transport.BarrierEnableEncryptionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierEnableEncryptionRequest{
		TenantId:        req.TenantID,
		Strategy:        req.Strategy,
		Mechanism:       req.Mechanism,
		Availability:    req.Availability,
		Passphrase:      req.Passphrase,
		ShamirThreshold: int32(req.ShamirThreshold),
		ShamirTotal:     int32(req.ShamirTotal),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.EnableEncryption", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierEnableEncryptionResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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
	// QUIC carries the SO PIN in the request message; see transport.WithSOPIN.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierGenerateRecoveryKeys", req.SOPin)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&pb.BarrierRecoveryGenerateRequest{
		Threshold: int32(req.Threshold), //nolint:gosec
		Policy:    req.Policy,
		SoPin:     soPIN,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.callWithSecret(ctx, "barrier.RecoveryGenerate", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierRecoveryKeysResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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
	// QUIC carries the SO PIN in the request message; see transport.WithSOPIN.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierDeleteRecoveryKeys", "")
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(&pb.BarrierRecoveryDeleteRequest{SoPin: soPIN})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.callWithSecret(ctx, "barrier.RecoveryDelete", payload)
	return err
}

// BarrierHasRecoveryKeys checks whether recovery keys have been generated.
func (t *Transport) BarrierHasRecoveryKeys(ctx context.Context) (*transport.BarrierHasRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "barrier.RecoveryStatus", nil)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierRecoveryStatusResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierHasRecoveryKeysResponse{
		HasKeys: resp.GetHasRecoveryKeys(),
	}, nil
}

// ---------------------------------------------------------------------------
// Share holders (Security Officers and key custodians)
// ---------------------------------------------------------------------------

// callPB sends in as method's protobuf payload and decodes the reply into
// out.
func (t *Transport) callPB(ctx context.Context, method string, in, out proto.Message) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(in)
	if err != nil {
		return &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, method, payload)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(respBytes, out); err != nil {
		return &unmarshalError{Err: err}
	}
	return nil
}

// nilBarrierRequest is the typed error a barrier holder method returns for a
// nil request, before any round trip.
func nilBarrierRequest(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}

// registerHolder enrols a holder through method.
func (t *Transport) registerHolder(ctx context.Context, method string, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	in, err := holderconv.RegisterToPB(req)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierOfficerRegisterResponse
	if err := t.callPB(ctx, method, in, &resp); err != nil {
		return nil, err
	}
	return holderconv.RegisterFromPB(&resp), nil
}

// OfficerRegister enrols a share holder of the unseal key.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil {
		return nil, nilBarrierRequest("OfficerRegister")
	}
	return t.registerHolder(ctx, "barrier.OfficerRegister", req)
}

// RecoveryOfficerRegister enrols a holder of the recovery key.
func (t *Transport) RecoveryOfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil {
		return nil, nilBarrierRequest("RecoveryOfficerRegister")
	}
	return t.registerHolder(ctx, "barrier.RecoveryOfficerRegister", req)
}

// OfficerList lists the unseal key's holders and their claim state.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	var resp pb.BarrierOfficerListResponse
	if err := t.callPB(ctx, "barrier.OfficerList", &emptypb.Empty{}, &resp); err != nil {
		return nil, err
	}
	return holderconv.StatusesFromPB(resp.GetOfficers()), nil
}

// claimBegin issues a claim nonce through method.
func (t *Transport) claimBegin(ctx context.Context, method string, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	var resp pb.BarrierShareClaimBeginResponse
	if err := t.callPB(ctx, method, &pb.BarrierShareClaimBeginRequest{Username: req.Username}, &resp); err != nil {
		return nil, err
	}
	return holderconv.ClaimBeginFromPB(resp.GetNonceHex(), resp.GetExpiresAt()), nil
}

// claimComplete delivers a holder's sealed envelope through method.
func (t *Transport) claimComplete(ctx context.Context, method string, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	var resp pb.BarrierShareClaimCompleteResponse
	in := &pb.BarrierShareClaimCompleteRequest{Username: req.Username, NonceHex: req.NonceHex, Signature: req.Signature}
	if err := t.callPB(ctx, method, in, &resp); err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: int(resp.GetShareIndex()), Envelope: resp.GetEnvelope()}, nil
}

// ShareClaimBegin issues a claim nonce to an unseal-key holder.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, nilBarrierRequest("ShareClaimBegin")
	}
	return t.claimBegin(ctx, "barrier.ShareClaimBegin", req)
}

// ShareClaimComplete delivers an unseal-key holder's sealed envelope once.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, nilBarrierRequest("ShareClaimComplete")
	}
	return t.claimComplete(ctx, "barrier.ShareClaimComplete", req)
}

// RecoveryShareClaimBegin issues a claim nonce to a recovery holder.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil {
		return nil, nilBarrierRequest("RecoveryShareClaimBegin")
	}
	return t.claimBegin(ctx, "barrier.RecoveryShareClaimBegin", req)
}

// RecoveryShareClaimComplete delivers a recovery holder's envelope once.
func (t *Transport) RecoveryShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil {
		return nil, nilBarrierRequest("RecoveryShareClaimComplete")
	}
	return t.claimComplete(ctx, "barrier.RecoveryShareClaimComplete", req)
}

// BarrierQuorumBegin issues the factor challenge a holder answers.
func (t *Transport) BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if req == nil {
		return nil, nilBarrierRequest("BarrierQuorumBegin")
	}
	var resp pb.BarrierQuorumChallenge
	if err := t.callPB(ctx, "barrier.QuorumBegin", holderconv.BeginToPB(req), &resp); err != nil {
		return nil, err
	}
	return holderconv.ChallengeFromPB(&resp), nil
}

// BarrierQuorumSubmit submits one authenticated holder's share.
func (t *Transport) BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if req == nil {
		return nil, nilBarrierRequest("BarrierQuorumSubmit")
	}
	var resp pb.BarrierQuorumOutcome
	if err := t.callPB(ctx, "barrier.QuorumSubmit", holderconv.SubmitToPB(req), &resp); err != nil {
		return nil, err
	}
	return holderconv.OutcomeFromPB(&resp), nil
}

// BarrierRekeyStart opens a rekey round.
func (t *Transport) BarrierRekeyStart(ctx context.Context, req *transport.RekeyStartRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, nilBarrierRequest("BarrierRekeyStart")
	}
	// The SO PIN travels in the request's own so_pin field; it comes from the
	// PIN bound by transport.WithSOPIN when the request carries none.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierRekeyStart", req.SOPin)
	if err != nil {
		return nil, err
	}
	resolved := *req
	resolved.SOPin = soPIN
	var resp pb.BarrierQuorumRound
	if err := t.callPB(ctx, "barrier.RekeyStart", holderconv.RekeyToPB(&resolved), &resp); err != nil {
		return nil, err
	}
	return holderconv.RoundFromPB(&resp), nil
}

// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey.
func (t *Transport) BarrierOfficerRevoke(ctx context.Context, req *transport.OfficerRevokeRequest) (*transport.QuorumRound, error) {
	if req == nil {
		return nil, nilBarrierRequest("BarrierOfficerRevoke")
	}
	// The SO PIN travels in the request's own so_pin field; it comes from the
	// PIN bound by transport.WithSOPIN when the request carries none.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierOfficerRevoke", req.SOPin)
	if err != nil {
		return nil, err
	}
	resolved := *req
	resolved.SOPin = soPIN
	var resp pb.BarrierQuorumRound
	if err := t.callPB(ctx, "barrier.OfficerRevoke", holderconv.RevokeToPB(&resolved), &resp); err != nil {
		return nil, err
	}
	return holderconv.RoundFromPB(&resp), nil
}
