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
		SOCertPEM:  resp.GetSoCertPem(),
		SOKeyPEM:   resp.GetSoKeyPem(),
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

// CeremonyClaimShare retrieves the Shamir share for an officer.
func (t *Transport) CeremonyClaimShare(ctx context.Context, req *transport.ClaimShareRequest) (*transport.ClaimShareResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CeremonyClaimShare(ctx, &pb.CeremonyClaimShareRequest{
		Username: req.Username,
	})
	if err != nil {
		return nil, mapGRPCError("CeremonyClaimShare", err)
	}
	return &transport.ClaimShareResponse{
		ShareJSON: resp.GetShare(),
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
