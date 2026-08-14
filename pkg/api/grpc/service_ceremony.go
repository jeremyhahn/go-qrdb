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
	"encoding/base64"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Ceremony handlers
// ---------------------------------------------------------------------------

// CeremonyInitialize performs the initialization ceremony, issuing TLS and
// SO certificates from the server's configured CeremonyCA (WithCeremonyCA for
// the TCP gRPC transport, WithRegisterCeremonyCA for the Unix domain socket
// transport, which shares this handler). The per-request SO and user PINs
// are forwarded so callers can supply credentials on the call itself.
func (s *qrdbServiceServer) CeremonyInitialize(ctx context.Context, req *pb.CeremonyInitializeRequest) (*pb.CeremonyInitializeResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}
	if s.ceremonyCA == nil {
		return nil, status.Error(codes.Unimplemented, (&CeremonyCANotConfiguredError{}).Error())
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
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyInitializeResponse{
		SpkiPin:    result.SPKIPin,
		CaCertPem:  string(result.CACertPEM),
		TlsCertPem: string(result.TLSCertPEM),
		State:      result.State,
		ShareCount: int32(result.ShareCount),
	}, nil
}

// CeremonyStatus returns the current ceremony state and SPKI pin.
func (s *qrdbServiceServer) CeremonyStatus(ctx context.Context, _ *emptypb.Empty) (*pb.CeremonyStatusResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	info, err := s.ceremonySvc.CeremonyStatus(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyStatusResponse{
		State:   info.State,
		SpkiPin: info.SPKIPin,
	}, nil
}

// CeremonyClaimCertBegin starts the certificate claim handshake for an
// officer, returning a challenge nonce and its expiration.
func (s *qrdbServiceServer) CeremonyClaimCertBegin(ctx context.Context, req *pb.CeremonyClaimCertBeginRequest) (*pb.CeremonyClaimCertBeginResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	challenge, err := s.ceremonySvc.CeremonyBeginClaimCert(ctx, req.GetUsername())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyClaimCertBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339Nano),
	}, nil
}

// CeremonyClaimCertComplete completes the certificate claim handshake by
// verifying the officer's nonce signature and returning the issued certificate.
func (s *qrdbServiceServer) CeremonyClaimCertComplete(ctx context.Context, req *pb.CeremonyClaimCertCompleteRequest) (*pb.CeremonyClaimCertCompleteResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	sig, err := base64.StdEncoding.DecodeString(req.GetSignature())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, (&SignatureDecodeError{Err: err}).Error())
	}

	result, err := s.ceremonySvc.CeremonyCompleteClaimCert(ctx, req.GetUsername(), req.GetNonceHex(), sig)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyClaimCertCompleteResponse{
		CertPem:   string(result.CertPEM),
		CaCertPem: string(result.CACertPEM),
	}, nil
}

// CeremonyBeginClaimShare starts the Shamir share claim handshake for the given
// officer. The caller MUST present an mTLS client certificate: its thumbprint
// is derived from the verified TLS peer chain (never from the request) and must
// match the thumbprint recorded for the officer at enrollment.
func (s *qrdbServiceServer) CeremonyBeginClaimShare(ctx context.Context, req *pb.CeremonyBeginClaimShareRequest) (*pb.CeremonyBeginClaimShareResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	challenge, err := s.ceremonySvc.CeremonyBeginClaimShare(ctx, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyBeginClaimShareResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	}, nil
}

// CeremonyCompleteClaimShare completes the Shamir share claim handshake. The
// officer's signature over the challenge nonce and the caller's mTLS
// certificate thumbprint are both verified before the share is released
// exactly once.
func (s *qrdbServiceServer) CeremonyCompleteClaimShare(ctx context.Context, req *pb.CeremonyCompleteClaimShareRequest) (*pb.CeremonyCompleteClaimShareResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	result, err := s.ceremonySvc.CeremonyCompleteClaimShare(ctx, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyCompleteClaimShareResponse{
		ShareIndex: int32(result.ShareIndex), //nolint:gosec // share index is a small positive ordinal
		Share:      string(result.ShareBytes),
	}, nil
}

// CeremonyEnrollExternalOfficer enrols an officer whose key material lives
// outside go-qrdb. Either CertPEM (validated against the configured CA pools)
// or PubKeyPEM (admin-asserted trust) MUST be supplied, but not both.
func (s *qrdbServiceServer) CeremonyEnrollExternalOfficer(ctx context.Context, req *pb.CeremonyEnrollExternalOfficerRequest) (*pb.CeremonyEnrollExternalOfficerResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	resp, err := s.ceremonySvc.CeremonyEnrollExternalOfficer(ctx, &service.CeremonyEnrollExternalOfficerRequest{
		Username:   req.GetUsername(),
		CertPEM:    req.GetCertPem(),
		PubKeyPEM:  req.GetPubkeyPem(),
		ShareIndex: int(req.GetShareIndex()),
		AuthFactor: req.GetAuthFactor(),
		ShareBytes: req.GetShareBytes(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyEnrollExternalOfficerResponse{
		SealedShare: resp.SealedShare,
	}, nil
}

// CeremonySignCSR signs a CSR during the ceremony for an officer or service,
// returning the issued certificate.
func (s *qrdbServiceServer) CeremonySignCSR(ctx context.Context, req *pb.CeremonySignCSRRequest) (*pb.CeremonySignCSRResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	result, err := s.ceremonySvc.CeremonySignCSR(ctx, req.GetUsername(), req.GetSoPin(), req.GetCsrPem(), req.GetRole())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonySignCSRResponse{
		CertPem: string(result.CertPEM),
	}, nil
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

// ---------------------------------------------------------------------------
// Ceremony error types
// ---------------------------------------------------------------------------

// SignatureDecodeError is returned when the base64-encoded signature from a
// gRPC request cannot be decoded.
type SignatureDecodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SignatureDecodeError) Error() string {
	return "grpc: failed to decode signature: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SignatureDecodeError) Unwrap() error {
	return e.Err
}
