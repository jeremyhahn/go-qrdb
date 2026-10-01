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
	// QUIC carries the SO PIN in the request message; see transport.WithSOPIN.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierInitializeShamir", req.SOPin)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&pb.BarrierShamirInitializeRequest{
		Secret:      req.Secret,
		Threshold:   int32(req.Threshold),
		TotalShares: int32(req.TotalShares),
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

// BarrierUnsealWithShare submits a single Shamir share toward the unseal
// threshold.
func (t *Transport) BarrierUnsealWithShare(ctx context.Context, req *transport.BarrierUnsealShareRequest) (*transport.BarrierUnsealShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShamirUnsealShareRequest{
		Share: req.Share,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.ShamirUnsealShare", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirUnsealShareResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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
	payload, err := proto.Marshal(&pb.BarrierShamirUnsealSharesRequest{
		Shares: req.Shares,
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "barrier.ShamirUnsealShares", payload)
	return err
}

// BarrierRekey generates new Shamir shares with the specified threshold and
// total count, replacing the existing shares.
func (t *Transport) BarrierRekey(ctx context.Context, req *transport.BarrierRekeyRequest) (*transport.BarrierRekeyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// QUIC carries the SO PIN in the request message; see transport.WithSOPIN.
	soPIN, err := transport.ResolveSOPIN(ctx, "BarrierRekey", req.SOPin)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&pb.BarrierRekeyRequest{
		Threshold: int32(req.Threshold),
		Total:     int32(req.Total),
		SoPin:     soPIN,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.callWithSecret(ctx, "barrier.Rekey", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirOperationResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierRekeyResponse{
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
		Threshold: int32(req.Threshold),
		Total:     int32(req.Total),
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

// BarrierRecoverWithKeys unseals the barrier using recovery keys.
func (t *Transport) BarrierRecoverWithKeys(ctx context.Context, req *transport.BarrierRecoverWithKeysRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierRecoveryRecoverRequest{
		Keys: req.Keys,
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "barrier.RecoveryRecover", payload)
	return err
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

// BarrierGenerateRootToken generates a new root token using the provided
// Shamir shares.
func (t *Transport) BarrierGenerateRootToken(ctx context.Context, req *transport.BarrierGenerateRootTokenRequest) (*transport.BarrierRootTokenResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierRootTokenRequest{
		Shares: req.Shares,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.RootToken", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierRootTokenResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierRootTokenResponse{
		Token:     resp.GetToken(),
		CreatedAt: resp.GetCreatedAt(),
	}, nil
}

// OfficerRegister registers a Security Officer for the system barrier.
func (t *Transport) OfficerRegister(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CsrPem:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.OfficerRegister", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierOfficerRegisterResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    resp.GetCertPem(),
		ShareIndex: int(resp.GetShareIndex()),
	}, nil
}

// OfficerList returns all enrolled Security Officers for the system barrier.
func (t *Transport) OfficerList(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "barrier.OfficerList", nil)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierOfficerListResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return quicProtoToOfficerStatusSlice(resp.GetOfficers()), nil
}

// OfficerClaimStatus returns a per-officer claim status snapshot for the
// system barrier. Never includes share bytes.
func (t *Transport) OfficerClaimStatus(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "barrier.OfficerClaimStatus", nil)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierOfficerClaimStatusResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return quicProtoToOfficerStatusSlice(resp.GetOfficers()), nil
}

// ShareClaimBegin issues a challenge nonce to the named Security Officer for
// the system barrier share claim handshake.
func (t *Transport) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShareClaimBeginRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.ShareClaimBegin", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShareClaimBeginResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, resp.GetExpiresAt())
	if parseErr != nil {
		expiresAt = time.Time{}
	}
	return &transport.ShareClaimBeginResponse{
		NonceHex:  resp.GetNonceHex(),
		ExpiresAt: expiresAt,
	}, nil
}

// ShareClaimComplete verifies the Security Officer's nonce signature and
// returns the system barrier share bytes exactly once.
func (t *Transport) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.ShareClaimComplete", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShareClaimCompleteResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: int(resp.GetShareIndex()),
		ShareBytes: resp.GetShareBytes(),
	}, nil
}

// ShamirSealVote submits a Shamir share as a seal vote for the system
// barrier. When the quorum threshold is met the barrier is sealed automatically.
func (t *Transport) ShamirSealVote(ctx context.Context, req *transport.ShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShamirSealVoteRequest{
		Share: req.Share,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.ShamirSealVote", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirSealVoteResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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
	payload, err := proto.Marshal(&pb.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CsrPem:            req.CSRPEM,
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.RecoveryOfficerRegister", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierOfficerRegisterResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.OfficerRegisterResponse{
		CertPEM:    resp.GetCertPem(),
		ShareIndex: int(resp.GetShareIndex()),
	}, nil
}

// RecoveryShareClaimBegin issues a challenge nonce to the named recovery
// officer for the recovery share claim handshake.
func (t *Transport) RecoveryShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShareClaimBeginRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.RecoveryShareClaimBegin", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShareClaimBeginResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, resp.GetExpiresAt())
	if parseErr != nil {
		expiresAt = time.Time{}
	}
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
	payload, err := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.RecoveryShareClaimComplete", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShareClaimCompleteResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.ShareClaimCompleteResponse{
		ShareIndex: int(resp.GetShareIndex()),
		ShareBytes: resp.GetShareBytes(),
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// quicProtoToOfficerStatusSlice converts a slice of proto OfficerStatus
// messages to transport OfficerStatus values.
func quicProtoToOfficerStatusSlice(officers []*pb.OfficerStatus) []*transport.OfficerStatus {
	out := make([]*transport.OfficerStatus, len(officers))
	for i, o := range officers {
		s := &transport.OfficerStatus{
			Username:       o.GetUsername(),
			CertThumbprint: o.GetCertThumbprint(),
			ShareIndex:     int(o.GetShareIndex()),
			AuthFactor:     o.GetAuthFactor(),
			SharePending:   o.GetSharePending(),
		}
		if raw := o.GetEnrolledAt(); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				s.EnrolledAt = t
			}
		}
		if raw := o.GetClaimedAt(); raw != "" {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				s.ClaimedAt = t
			}
		}
		out[i] = s
	}
	return out
}
