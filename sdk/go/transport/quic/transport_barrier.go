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
		Secret: req.Secret,
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
	_, err := t.call(ctx, "barrier.Seal", nil)
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
		Sealed:         resp.GetSealed(),
		Strategy:       resp.GetStrategy(),
		HardwareBacked: resp.GetHardwareBacked(),
		InitializedAt:  resp.GetInitializedAt(),
	}, nil
}

// BarrierInitializeShamir initializes the barrier using Shamir secret sharing
// and returns the generated key shares.
func (t *Transport) BarrierInitializeShamir(ctx context.Context, req *transport.BarrierInitializeShamirRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShamirInitializeRequest{
		Secret:      req.Secret,
		Threshold:   int32(req.Threshold),
		TotalShares: int32(req.TotalShares),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.ShamirInitialize", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirInitializeResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierInitializeShamirResponse{
		Shares:      resp.GetShares(),
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
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

// BarrierShamirListShares returns metadata about stored Shamir shares.
func (t *Transport) BarrierShamirListShares(ctx context.Context) (*transport.BarrierShamirSharesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respBytes, err := t.call(ctx, "barrier.ShamirListShares", nil)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirSharesResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierShamirSharesResponse{
		Count:     int(resp.GetCount()),
		Threshold: int(resp.GetThreshold()),
		Total:     int(resp.GetTotal()),
	}, nil
}

// BarrierShamirDeleteShare deletes a single Shamir share by index.
func (t *Transport) BarrierShamirDeleteShare(ctx context.Context, req *transport.BarrierShamirDeleteShareRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierShamirDeleteShareRequest{
		Index: int32(req.Index),
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "barrier.ShamirDeleteShare", payload)
	return err
}

// BarrierShamirDeleteAllShares deletes all stored Shamir shares.
func (t *Transport) BarrierShamirDeleteAllShares(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.call(ctx, "barrier.ShamirDeleteAllShares", nil)
	return err
}

// BarrierShamirVerify verifies the integrity of stored Shamir shares.
func (t *Transport) BarrierShamirVerify(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.call(ctx, "barrier.ShamirVerify", nil)
	return err
}

// BarrierRekey generates new Shamir shares with the specified threshold and
// total count, replacing the existing shares.
func (t *Transport) BarrierRekey(ctx context.Context, req *transport.BarrierRekeyRequest) (*transport.BarrierRekeyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierRekeyRequest{
		Threshold: int32(req.Threshold),
		Total:     int32(req.Total),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.Rekey", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierShamirInitializeResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierRekeyResponse{
		Shares:      resp.GetShares(),
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
	}, nil
}

// BarrierGenerateRecoveryKeys generates recovery keys for disaster recovery
// scenarios.
func (t *Transport) BarrierGenerateRecoveryKeys(ctx context.Context, req *transport.BarrierGenerateRecoveryKeysRequest) (*transport.BarrierRecoveryKeysResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.BarrierRecoveryGenerateRequest{
		Threshold: int32(req.Threshold),
		Total:     int32(req.Total),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "barrier.RecoveryGenerate", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.BarrierRecoveryKeysResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.BarrierRecoveryKeysResponse{
		Keys:      resp.GetKeys(),
		Threshold: int(resp.GetThreshold()),
		Total:     int(resp.GetTotal()),
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
	_, err := t.call(ctx, "barrier.RecoveryDelete", nil)
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
