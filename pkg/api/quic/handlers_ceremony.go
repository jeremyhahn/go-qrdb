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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleCeremonyInitialize performs the initialization ceremony by delegating
// to the ceremony service layer. The QUIC transport passes nil for the
// CeremonyCA parameter since it does not have access to the CA instance
// directly, matching the gRPC transport behavior. The per-request SO and
// user PINs are decoded from the request payload and forwarded to the
// service layer.
func (s *Server) handleCeremonyInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	var req pb.CeremonyInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.ceremonySvc.CeremonyInitialize(ctx, nil, &service.CeremonyInitializeOptions{
		SOPin:   req.GetSoPin(),
		UserPin: req.GetUserPin(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyInitializeResponse{
		SpkiPin:    result.SPKIPin,
		CaCertPem:  string(result.CACertPEM),
		TlsCertPem: string(result.TLSCertPEM),
		SoCertPem:  string(result.SOCertPEM),
		SoKeyPem:   string(result.SOKeyPEM),
		State:      result.State,
		ShareCount: int32(result.ShareCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCeremonyStatus returns the current ceremony state and SPKI pin.
func (s *Server) handleCeremonyStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	info, err := s.ceremonySvc.CeremonyStatus(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyStatusResponse{
		State:   info.State,
		SpkiPin: info.SPKIPin,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCeremonyClaimCertBegin starts the certificate claim handshake for
// the given officer username, returning a challenge nonce and expiration.
func (s *Server) handleCeremonyClaimCertBegin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	var req pb.CeremonyClaimCertBeginRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	challenge, err := s.ceremonySvc.CeremonyBeginClaimCert(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyClaimCertBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCeremonyClaimCertComplete completes the certificate claim handshake
// by verifying the officer's nonce signature and returning the certificate
// and CA certificate PEM.
func (s *Server) handleCeremonyClaimCertComplete(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	var req pb.CeremonyClaimCertCompleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.ceremonySvc.CeremonyCompleteClaimCert(ctx, req.GetUsername(), req.GetNonceHex(), []byte(req.GetSignature()))
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyClaimCertCompleteResponse{
		CertPem:   string(result.CertPEM),
		CaCertPem: string(result.CACertPEM),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCeremonyClaimShare retrieves the Shamir share for the given officer
// username and returns it as a string.
func (s *Server) handleCeremonyClaimShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	var req pb.CeremonyClaimShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	shareBytes, err := s.ceremonySvc.CeremonyClaimShare(ctx, req.GetUsername())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyClaimShareResponse{
		Share: string(shareBytes),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCeremonySignCSR signs a CSR during the ceremony for an officer or
// service, returning the issued certificate PEM.
func (s *Server) handleCeremonySignCSR(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	var req pb.CeremonySignCSRRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.ceremonySvc.CeremonySignCSR(ctx, req.GetUsername(), req.GetSoPin(), req.GetCsrPem(), req.GetRole())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonySignCSRResponse{
		CertPem: string(result.CertPEM),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
