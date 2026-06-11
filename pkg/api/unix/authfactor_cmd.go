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
// directly. It is the companion to audit_cmd.go and gives protocol parity for
// the AuthFactor service.
//
// The actual gRPC service registration is wired in server.go via
// WithAuthFactorService — the AuthFactor gRPC service is registered on the
// Unix socket whenever a service.AuthFactorServicer is provided, the same way
// the AuditService is registered when an audit.AuditLogger is provided.
//
// Authorization (when permission constants are wired into the Unix interceptor
// chain):
//   - EnrollFactor / RevokeFactor: SO (CeremonyEnrollCustodian /
//     CeremonyRevokeCustodian).
//   - ChallengeFactor / VerifyFactor: enrolled custodian
//     (CeremonyClaimShareBegin / CeremonyClaimShareComplete).
//   - ListFactors: AuditRead (Auditor) or SO.
package unix

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// The auth factor wire types are the canonical qrdb.v1 protobuf messages.
// These Unix-package aliases let callers reference them without importing
// the qrdbv1 package directly.

// AuthFactorEnrollRequest is the protobuf enrollment payload for Unix callers.
type AuthFactorEnrollRequest = pb.EnrollFactorRequest

// AuthFactorEnrollResponse is the protobuf enrollment response for Unix callers.
type AuthFactorEnrollResponse = pb.EnrollFactorResponse

// AuthFactorChallengeRequest is the protobuf challenge request for Unix callers.
type AuthFactorChallengeRequest = pb.ChallengeFactorRequest

// AuthFactorChallengeResponse is the protobuf challenge response for Unix callers.
type AuthFactorChallengeResponse = pb.ChallengeFactorResponse

// AuthFactorVerifyRequest is the protobuf verify payload for Unix callers.
type AuthFactorVerifyRequest = pb.VerifyFactorRequest

// AuthFactorListRequest is the protobuf list filter for Unix callers.
type AuthFactorListRequest = pb.ListFactorsRequest

// AuthFactorListResponse is the protobuf enrollment list returned to Unix callers.
type AuthFactorListResponse = pb.ListFactorsResponse

// AuthFactorRevokeRequest is the protobuf revoke payload for Unix callers.
type AuthFactorRevokeRequest = pb.RevokeFactorRequest
