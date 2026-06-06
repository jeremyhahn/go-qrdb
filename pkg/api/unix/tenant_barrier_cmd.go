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
	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
)

// TenantBarrierInitRequest is the JSON-encoded init payload for Unix callers.
// It mirrors grpcpkg.GRPCTenantBarrierInitRequest for type consistency with
// the gRPC service registered on the Unix socket.
type TenantBarrierInitRequest = grpcpkg.GRPCTenantBarrierInitRequest

// TenantBarrierUnsealRequest is the JSON-encoded unseal payload for Unix
// callers. It mirrors grpcpkg.GRPCTenantBarrierUnsealRequest.
type TenantBarrierUnsealRequest = grpcpkg.GRPCTenantBarrierUnsealRequest

// TenantBarrierSealRequest is the JSON-encoded seal payload for Unix
// callers. It mirrors grpcpkg.GRPCTenantBarrierSealRequest.
type TenantBarrierSealRequest = grpcpkg.GRPCTenantBarrierSealRequest

// TenantBarrierStatusRequest is the JSON-encoded status query for Unix
// callers. It mirrors grpcpkg.GRPCTenantBarrierStatusRequest.
type TenantBarrierStatusRequest = grpcpkg.GRPCTenantBarrierStatusRequest

// TenantBarrierStatusResponse is the JSON-encoded status response returned
// to Unix callers. It mirrors grpcpkg.GRPCTenantBarrierStatusResponse.
type TenantBarrierStatusResponse = grpcpkg.GRPCTenantBarrierStatusResponse
