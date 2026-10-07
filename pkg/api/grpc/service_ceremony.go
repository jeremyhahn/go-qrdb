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
	"errors"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
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
//
// s.ceremonyCA is nil whenever this node's TLS identity is BYO (no CA
// private key configured) -- that is no longer refused here.
// ceremony.CeremonyService.Initialize supports ca == nil directly (the
// operator's own leaf certificate is the server identity, and no CA is
// needed to pin it), refusing only the two admin-credential paths that
// actually need one to sign with (CSR-sign, generate) via the typed
// *ceremony.AdminCredentialRequiresCAError the service layer already
// returns (go-qrdb#560 review item 5).
func (s *qrdbServiceServer) CeremonyInitialize(ctx context.Context, req *pb.CeremonyInitializeRequest) (*pb.CeremonyInitializeResponse, error) {
	if s.ceremonySvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "ceremony"})
	}
	// req.Secret (the proto request's own bytes) is consumed into a
	// *secure.Buffer by CeremonyInitFromPB, which this handler owns and
	// destroys once the service call below has finished with it.
	opts, err := holderpb.CeremonyInitFromPB(req)
	if err != nil {
		return nil, mapServiceError(err)
	}
	defer opts.Secret.Destroy()

	result, err := s.ceremonySvc.CeremonyInitialize(ctx, s.ceremonyCA, opts)
	if err != nil {
		// The admin-credential step must commit on the system shard's
		// leader (service.CeremonyServiceWithLeaderGate); a follower
		// forwards the identical request through the SAME
		// ReplicaForwarder the generic per-database path uses, rather
		// than answering its own refusal (node parity, owner ruling
		// 2026-10-06). Unix shares this handler and gains the fix the
		// same way: it registers its own ReplicaForwarder via
		// WithRegisterReplicaForwarding.
		var notLeader *service.AdminCredentialNotLeaderError
		if errors.As(err, &notLeader) && s.replicaForwarder != nil {
			if reply, ferr, forwarded := s.replicaForwarder.forwardAdminWrite(ctx, pb.QRDBService_CeremonyInitialize_FullMethodName, req, notLeader.LeaderAddress); forwarded {
				if ferr != nil {
					return nil, ferr
				}
				if resp, ok := reply.(*pb.CeremonyInitializeResponse); ok {
					return resp, nil
				}
			}
		}
		return nil, mapServiceError(err)
	}
	// resp owns an independent bytes.Clone of result.AdminKeyPEM
	// (holderpb.CeremonyInitToPB's own doc comment), so wiping result's
	// copy here, now that resp is fully built, cannot zero resp's bytes
	// out from under the still-pending marshal and send
	// (go-qrdb#560 review item 7: every transport, not only REST).
	resp := holderpb.CeremonyInitToPB(result)
	result.WipeAdminKeyPEM()
	return resp, nil
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
