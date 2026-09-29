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
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "ceremony"})
	}
	if s.ceremonyCA == nil {
		return nil, mapServiceError(&CeremonyCANotConfiguredError{})
	}

	result, err := s.ceremonySvc.CeremonyInitialize(ctx, s.ceremonyCA, holderpb.CeremonyInitFromPB(req))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.CeremonyInitToPB(result), nil
}

// CeremonyStatus returns the current ceremony state and SPKI pin.
func (s *qrdbServiceServer) CeremonyStatus(ctx context.Context, _ *emptypb.Empty) (*pb.CeremonyStatusResponse, error) {
	if s.ceremonySvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "ceremony"})
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

// CeremonySignCSR signs a CSR during the ceremony for an officer or service,
// returning the issued certificate.
func (s *qrdbServiceServer) CeremonySignCSR(ctx context.Context, req *pb.CeremonySignCSRRequest) (*pb.CeremonySignCSRResponse, error) {
	if s.ceremonySvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "ceremony"})
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
