// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package unix provides a gRPC server over Unix domain sockets for QRDB.
// This file exposes the per-tenant barrier service wire types as Unix-package
// aliases for callers that talk to the Unix endpoint without importing the
// gRPC package directly. It is the companion to audit_cmd.go and
// authfactor_cmd.go and gives the per-tenant barrier RPCs protocol parity
// over the Unix socket.
//
// The actual gRPC service registration is wired in server.go via
// WithTenantBarrierService — the TenantBarrierService gRPC service is
// registered on the Unix socket whenever a tenant.TenantServicer is
// provided, the same way the AuditService is registered when an
// audit.AuditLogger is provided.
//
// Authorization (when Phase 2 permission constants are wired into the Unix
// interceptor chain):
//   - Init / Unseal / Seal: SO (BarrierUnseal / BarrierSeal class).
//   - Status: any authenticated tenant operator (TenantRead).
package unix

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// The per-tenant barrier wire types are the canonical qrdb.v1 protobuf
// messages. These Unix-package aliases let callers that talk to the Unix
// endpoint reference them without importing the qrdbv1 package directly.

// TenantBarrierInitRequest is the protobuf init payload for Unix callers.
type TenantBarrierInitRequest = pb.TenantBarrierInitRequest

// TenantBarrierInitResponse is the protobuf init response for Unix callers.
// Share bytes are never present; Security Officers claim their shares via the
// OfficerRegister / ShareClaimBegin / ShareClaimComplete flow.
type TenantBarrierInitResponse = pb.TenantBarrierInitResponse

// TenantBarrierUnsealRequest is the protobuf single-key unseal payload.
type TenantBarrierUnsealRequest = pb.TenantBarrierUnsealRequest

// TenantBarrierUnsealShareRequest is the protobuf Shamir-share unseal payload.
type TenantBarrierUnsealShareRequest = pb.TenantBarrierUnsealShareRequest

// TenantBarrierUnsealProgress is the protobuf Shamir quorum-progress response.
type TenantBarrierUnsealProgress = pb.TenantBarrierUnsealProgress

// TenantBarrierSealRequest is the protobuf seal payload.
type TenantBarrierSealRequest = pb.TenantBarrierSealRequest

// TenantBarrierStatusRequest is the protobuf status query.
type TenantBarrierStatusRequest = pb.TenantBarrierStatusRequest

// TenantBarrierStatusResponse is the protobuf status response.
type TenantBarrierStatusResponse = pb.TenantBarrierStatusResponse

// ---------------------------------------------------------------------------
// Security Officer wire types
// ---------------------------------------------------------------------------

// TenantBarrierOfficerRegisterRequest is the protobuf SO registration request.
type TenantBarrierOfficerRegisterRequest = pb.TenantBarrierOfficerRegisterRequest

// TenantBarrierOfficerRegisterResponse is the protobuf SO registration response.
type TenantBarrierOfficerRegisterResponse = pb.TenantBarrierOfficerRegisterResponse

// TenantBarrierOfficerListRequest is the protobuf SO list request.
type TenantBarrierOfficerListRequest = pb.TenantBarrierOfficerListRequest

// TenantBarrierOfficerListResponse is the protobuf SO list response.
type TenantBarrierOfficerListResponse = pb.TenantBarrierOfficerListResponse

// TenantBarrierShareClaimBeginRequest is the protobuf share-claim begin request.
type TenantBarrierShareClaimBeginRequest = pb.TenantBarrierShareClaimBeginRequest

// TenantBarrierShareClaimBeginResponse is the protobuf share-claim begin response.
type TenantBarrierShareClaimBeginResponse = pb.TenantBarrierShareClaimBeginResponse

// TenantBarrierShareClaimCompleteRequest is the protobuf share-claim complete request.
type TenantBarrierShareClaimCompleteRequest = pb.TenantBarrierShareClaimCompleteRequest

// TenantBarrierShareClaimCompleteResponse is the protobuf share-claim complete
// response. This is the sole wire surface where share bytes appear
// (INV-1/INV-11) — only in the claiming officer's own authenticated session.
type TenantBarrierShareClaimCompleteResponse = pb.TenantBarrierShareClaimCompleteResponse

// TenantBarrierOfficerClaimStatusRequest is the protobuf officer claim status request.
type TenantBarrierOfficerClaimStatusRequest = pb.TenantBarrierOfficerClaimStatusRequest

// TenantBarrierOfficerClaimStatusResponse is the protobuf officer claim status response.
type TenantBarrierOfficerClaimStatusResponse = pb.TenantBarrierOfficerClaimStatusResponse

// TenantBarrierShamirSealVoteRequest is the protobuf Shamir seal-vote request.
type TenantBarrierShamirSealVoteRequest = pb.TenantBarrierShamirSealVoteRequest

// TenantBarrierShamirSealVoteResponse is the protobuf Shamir seal-vote response.
type TenantBarrierShamirSealVoteResponse = pb.TenantBarrierShamirSealVoteResponse
