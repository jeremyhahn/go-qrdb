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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CeremonyClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CeremonyClient implementation
// ---------------------------------------------------------------------------

// CeremonyInitialize starts the initialization ceremony.
func (t *Transport) CeremonyInitialize(ctx context.Context, req *transport.CeremonyInitializeRequest) (*transport.CeremonyInitializeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	officers := make([]*pb.CeremonyOfficer, len(req.Officers))
	for i, o := range req.Officers {
		officers[i] = &pb.CeremonyOfficer{
			Username: o.Username,
			CsrPem:   o.CSRPEM,
		}
	}
	resp, err := t.client.CeremonyInitialize(ctx, &pb.CeremonyInitializeRequest{
		SoPin:                  req.SOPin,
		UserPin:                req.UserPin,
		Threshold:              int32(req.Threshold),
		CredentialSealStrategy: req.CredentialSealStrategy,
		Officers:               officers,
		Hostname:               req.Hostname,
		Sans:                   req.SANs,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyInitialize", err)
	}
	return &transport.CeremonyInitializeResponse{
		SPKIPin:    resp.GetSpkiPin(),
		CACertPEM:  resp.GetCaCertPem(),
		TLSCertPEM: resp.GetTlsCertPem(),
		State:      resp.GetState(),
		ShareCount: int(resp.GetShareCount()),
	}, nil
}

// CeremonyStatus returns the current ceremony state.
func (t *Transport) CeremonyStatus(ctx context.Context) (*transport.CeremonyStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyStatus(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("CeremonyStatus", err)
	}
	return &transport.CeremonyStatusResponse{
		State:   resp.GetState(),
		SPKIPin: resp.GetSpkiPin(),
	}, nil
}

// CeremonyClaimCertBegin starts a certificate claim challenge for the named
// officer.
func (t *Transport) CeremonyClaimCertBegin(ctx context.Context, req *transport.ClaimCertBeginRequest) (*transport.ClaimCertBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyClaimCertBegin(ctx, &pb.CeremonyClaimCertBeginRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyClaimCertBegin", err)
	}
	return &transport.ClaimCertBeginResponse{
		NonceHex:  resp.GetNonceHex(),
		ExpiresAt: resp.GetExpiresAt(),
	}, nil
}

// CeremonyClaimCertComplete completes a certificate claim by submitting the
// signed nonce.
func (t *Transport) CeremonyClaimCertComplete(ctx context.Context, req *transport.ClaimCertCompleteRequest) (*transport.ClaimCertCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyClaimCertComplete(ctx, &pb.CeremonyClaimCertCompleteRequest{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyClaimCertComplete", err)
	}
	return &transport.ClaimCertCompleteResponse{
		CertPEM:   resp.GetCertPem(),
		CACertPEM: resp.GetCaCertPem(),
	}, nil
}

// CeremonyBeginClaimShare starts the Shamir share claim handshake for an
// officer. The caller's certificate thumbprint is derived server-side from the
// mTLS peer chain, so the connection MUST carry a client certificate.
func (t *Transport) CeremonyBeginClaimShare(ctx context.Context, req *transport.BeginClaimShareRequest) (*transport.BeginClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyBeginClaimShare(ctx, &pb.CeremonyBeginClaimShareRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyBeginClaimShare", err)
	}
	return &transport.BeginClaimShareResponse{
		NonceHex:  resp.GetNonceHex(),
		ExpiresAt: resp.GetExpiresAt(),
	}, nil
}

// CeremonyCompleteClaimShare completes the Shamir share claim handshake by
// submitting the signed challenge nonce.
func (t *Transport) CeremonyCompleteClaimShare(ctx context.Context, req *transport.CompleteClaimShareRequest) (*transport.CompleteClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyCompleteClaimShare(ctx, &pb.CeremonyCompleteClaimShareRequest{
		Username:  req.Username,
		NonceHex:  req.NonceHex,
		Signature: req.Signature,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyCompleteClaimShare", err)
	}
	return &transport.CompleteClaimShareResponse{
		ShareIndex: int(resp.GetShareIndex()),
		ShareJSON:  resp.GetShare(),
	}, nil
}

// CeremonySignCSR submits a CSR for signing during or after the ceremony.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonySignCSR(ctx, &pb.CeremonySignCSRRequest{
		Username: req.Username,
		SoPin:    req.SOPin,
		CsrPem:   req.CSRPEM,
		Role:     req.Role,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonySignCSR", err)
	}
	return &transport.SignCSRInitResponse{
		CertPEM: resp.GetCertPem(),
	}, nil
}

// CeremonyEnrollExternalOfficer enrols an officer whose key material lives
// outside go-qrdb (external CA cert or bare public key).
func (t *Transport) CeremonyEnrollExternalOfficer(ctx context.Context, req *transport.EnrollExternalOfficerRequest) (*transport.EnrollExternalOfficerResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyEnrollExternalOfficer(ctx, &pb.CeremonyEnrollExternalOfficerRequest{
		Username:   req.Username,
		CertPem:    req.CertPEM,
		PubkeyPem:  req.PubKeyPEM,
		ShareIndex: int32(req.ShareIndex),
		AuthFactor: req.AuthFactor,
		ShareBytes: req.ShareBytes,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyEnrollExternalOfficer", err)
	}
	return &transport.EnrollExternalOfficerResponse{
		SealedShare: resp.GetSealedShare(),
	}, nil
}
