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
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
)

// handleCeremonyInitialize performs the initialization ceremony by delegating
// to the ceremony service layer, issuing TLS and SO certificates from the
// server's configured CeremonyCA (WithCeremonyCA), mirroring the gRPC, REST,
// and MCP transports. The per-request SO and user PINs are decoded from the
// request payload and forwarded to the service layer.
//
// s.ceremonyCA is nil whenever this node's TLS identity is BYO (no CA
// private key configured) -- that is no longer refused here.
// ceremony.CeremonyService.Initialize supports ca == nil directly (the
// operator's own leaf certificate is the server identity, and no CA is
// needed to pin it), refusing only the two admin-credential paths that
// actually need one to sign with (CSR-sign, generate) via the typed
// *ceremony.AdminCredentialRequiresCAError the service layer already
// returns (go-qrdb#560 review item 5).
func (s *Server) handleCeremonyInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.ceremonySvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "ceremony"}
	}

	var req pb.CeremonyInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.ceremonySvc.CeremonyInitialize(ctx, s.ceremonyCA, holderpb.CeremonyInitFromPB(&req))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.CeremonyInitToPB(result))
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
