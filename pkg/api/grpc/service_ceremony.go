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
)

// ---------------------------------------------------------------------------
// Ceremony handlers
// ---------------------------------------------------------------------------

// CeremonyInitialize performs the initialization ceremony. The gRPC transport
// does not carry a CeremonyCA reference; nil is passed to the service layer
// which uses its internally configured CA. Callers requiring explicit CA
// injection should use the REST API.
func (s *qrdbServiceServer) CeremonyInitialize(ctx context.Context, _ *pb.CeremonyInitializeRequest) (*pb.CeremonyInitializeResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	result, err := s.ceremonySvc.CeremonyInitialize(ctx, nil)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyInitializeResponse{
		SpkiPin:    result.SPKIPin,
		CaCertPem:  string(result.CACertPEM),
		TlsCertPem: string(result.TLSCertPEM),
		SoCertPem:  string(result.SOCertPEM),
		SoKeyPem:   string(result.SOKeyPEM),
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

// CeremonyClaimShare retrieves the Shamir share assigned to the given officer.
func (s *qrdbServiceServer) CeremonyClaimShare(ctx context.Context, req *pb.CeremonyClaimShareRequest) (*pb.CeremonyClaimShareResponse, error) {
	if s.ceremonySvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "ceremony"}).Error())
	}

	shareBytes, err := s.ceremonySvc.CeremonyClaimShare(ctx, req.GetUsername())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.CeremonyClaimShareResponse{
		Share: string(shareBytes),
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
