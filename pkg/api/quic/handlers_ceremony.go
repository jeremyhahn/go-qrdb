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
// to the ceremony service layer, issuing TLS and SO certificates from the
// server's configured CeremonyCA (WithCeremonyCA), mirroring the gRPC, REST,
// and MCP transports. The per-request SO and user PINs are decoded from the
// request payload and forwarded to the service layer.
func (s *Server) handleCeremonyInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}
	if s.ceremonyCA == nil {
		return nil, &CeremonyCANotConfiguredError{}
	}

	var req pb.CeremonyInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.ceremonySvc.CeremonyInitialize(ctx, s.ceremonyCA, &service.CeremonyInitializeOptions{
		SOPin:                  req.GetSoPin(),
		UserPin:                req.GetUserPin(),
		CredentialSealStrategy: req.GetCredentialSealStrategy(),
		Threshold:              int(req.GetThreshold()),
		Officers:               ceremonyOfficerConfigsFromPB(req.GetOfficers()),
		Hostname:               req.GetHostname(),
		SANs:                   req.GetSans(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyInitializeResponse{
		SpkiPin:    result.SPKIPin,
		CaCertPem:  string(result.CACertPEM),
		TlsCertPem: string(result.TLSCertPEM),
		State:      result.State,
		ShareCount: int32(result.ShareCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// ceremonyOfficerConfigsFromPB converts the protobuf officer list to the
// service-layer type. Returns nil when officers is empty so the service
// layer's "non-empty overrides the configured value" precedence rule works
// unchanged.
func ceremonyOfficerConfigsFromPB(officers []*pb.CeremonyOfficer) []service.CeremonyOfficerConfig {
	if len(officers) == 0 {
		return nil
	}
	out := make([]service.CeremonyOfficerConfig, len(officers))
	for i, o := range officers {
		out[i] = service.CeremonyOfficerConfig{Username: o.GetUsername(), CSRPEM: o.GetCsrPem()}
	}
	return out
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

// handleCeremonyBeginClaimShare starts the Shamir share claim handshake for the
// given officer. The caller MUST present an mTLS client certificate: its
// thumbprint is derived from the verified TLS peer chain (never from the
// request payload) and must match the enrolled officer record.
func (s *Server) handleCeremonyBeginClaimShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}

	var req pb.CeremonyBeginClaimShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	challenge, err := s.ceremonySvc.CeremonyBeginClaimShare(ctx, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyBeginClaimShareResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCeremonyCompleteClaimShare completes the Shamir share claim handshake.
// The officer's nonce signature and the caller's mTLS certificate thumbprint
// are both verified before the share is released exactly once.
func (s *Server) handleCeremonyCompleteClaimShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}

	var req pb.CeremonyCompleteClaimShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.ceremonySvc.CeremonyCompleteClaimShare(ctx, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CeremonyCompleteClaimShareResponse{
		ShareIndex: int32(result.ShareIndex), //nolint:gosec // share index is a small positive ordinal
		Share:      string(result.ShareBytes),
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
