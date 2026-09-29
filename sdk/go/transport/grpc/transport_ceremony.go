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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
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
	resp, err := t.client.CeremonyInitialize(ctx, holderconv.CeremonyInitToPB(req))
	if err != nil {
		return nil, mapGRPCError("CeremonyInitialize", err)
	}
	return holderconv.CeremonyInitFromPB(resp), nil
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

// CeremonySignCSR submits a CSR for signing during or after the ceremony.
func (t *Transport) CeremonySignCSR(ctx context.Context, req *transport.SignCSRInitRequest) (*transport.SignCSRInitResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// The SO PIN travels in the request's own so_pin field too, which the
	// server reads; it comes from the PIN bound by transport.WithSOPIN when
	// the request carries none, exactly as on QUIC.
	soPIN, err := transport.ResolveSOPIN(ctx, "CeremonySignCSR", req.SOPin)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.CeremonySignCSR(ctx, &pb.CeremonySignCSRRequest{
		Username: req.Username,
		SoPin:    soPIN,
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
