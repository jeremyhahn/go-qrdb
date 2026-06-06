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

// Package unix provides a gRPC server over Unix domain sockets for QRDB. This
// file exposes the auth factor service wire types as Unix-package aliases for
// callers that talk to the Unix endpoint without importing the gRPC package
// directly. It is the companion to audit_cmd.go and gives Phase 3 protocol
// parity for the AuthFactor service.
//
// The actual gRPC service registration is wired in server.go via
// WithAuthFactorService — the AuthFactor gRPC service is registered on the
// Unix socket whenever a service.AuthFactorServicer is provided, the same way
// the AuditService is registered when an audit.AuditLogger is provided.
//
// Authorization (when Phase 2 permission constants are wired into the Unix
// interceptor chain):
//   - EnrollFactor / RevokeFactor: SO (CeremonyEnrollCustodian /
//     CeremonyRevokeCustodian).
//   - ChallengeFactor / VerifyFactor: enrolled custodian
//     (CeremonyClaimShareBegin / CeremonyClaimShareComplete).
//   - ListFactors: AuditRead (Auditor) or SO.
package unix

import (
	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
)

// AuthFactorEnrollRequest is the JSON-encoded enrollment payload for Unix
// callers. It mirrors grpcpkg.GRPCEnrollFactorRequest for type consistency
// with the gRPC service registered on the Unix socket.
type AuthFactorEnrollRequest = grpcpkg.GRPCEnrollFactorRequest

// AuthFactorEnrollResponse is the JSON-encoded enrollment response for Unix
// callers. It mirrors grpcpkg.GRPCEnrollFactorResponse.
type AuthFactorEnrollResponse = grpcpkg.GRPCEnrollFactorResponse

// AuthFactorChallengeRequest is the JSON-encoded challenge request for Unix
// callers. It mirrors grpcpkg.GRPCChallengeFactorRequest.
type AuthFactorChallengeRequest = grpcpkg.GRPCChallengeFactorRequest

// AuthFactorChallengeResponse is the JSON-encoded challenge response for Unix
// callers. It mirrors grpcpkg.GRPCChallengeFactorResponse.
type AuthFactorChallengeResponse = grpcpkg.GRPCChallengeFactorResponse

// AuthFactorVerifyRequest is the JSON-encoded verify payload for Unix
// callers. It mirrors grpcpkg.GRPCVerifyFactorRequest.
type AuthFactorVerifyRequest = grpcpkg.GRPCVerifyFactorRequest

// AuthFactorListRequest is the JSON-encoded list filter for Unix callers. It
// mirrors grpcpkg.GRPCListFactorsRequest.
type AuthFactorListRequest = grpcpkg.GRPCListFactorsRequest

// AuthFactorListResponse is the JSON-encoded enrollment list returned to Unix
// callers. It mirrors grpcpkg.GRPCListFactorsResponse.
type AuthFactorListResponse = grpcpkg.GRPCListFactorsResponse

// AuthFactorRevokeRequest is the JSON-encoded revoke payload for Unix
// callers. It mirrors grpcpkg.GRPCRevokeFactorRequest.
type AuthFactorRevokeRequest = grpcpkg.GRPCRevokeFactorRequest
