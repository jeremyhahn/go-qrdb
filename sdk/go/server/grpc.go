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

package server

import qrdbgrpc "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"

// ---------------------------------------------------------------------------
// gRPC service registration
//
// QuicBaaS registers the qrdb gRPC service surface onto its own *grpc.Server.
// Re-exported so the composer never imports pkg/api/grpc directly.
// ---------------------------------------------------------------------------

// GRPCRegisterOption configures optional service dependencies for
// RegisterGRPCService. (Aliases pkg/api/grpc.RegisterOption.)
type GRPCRegisterOption = qrdbgrpc.RegisterOption

// RegisterGRPCService registers the qrdb gRPC service onto the given
// *grpc.Server. (Aliases pkg/api/grpc.RegisterService.)
var RegisterGRPCService = qrdbgrpc.RegisterService

// WithRegisterAdminService attaches the cluster admin service (status, peers,
// membership add/remove) to the registered qrdb gRPC service. An embedding
// daemon (e.g. xkmsd) uses this to host qrdb's own cluster control plane on its
// server instead of authoring a parallel one.
var WithRegisterAdminService = qrdbgrpc.WithRegisterAdminService

// WithRegisterRebalanceService attaches the shard rebalance service (stats,
// plan, execute, imbalance report) to the registered qrdb gRPC service. Pairs
// with Node.RebalanceService for composers hosting qrdb's control plane.
var WithRegisterRebalanceService = qrdbgrpc.WithRegisterRebalanceService

// WithRegisterBarrierService attaches a barrier service to the registered
// qrdb gRPC service.
var WithRegisterBarrierService = qrdbgrpc.WithRegisterBarrierService

// WithRegisterPINBackend attaches the PIN backend that gates the barrier
// administrative RPCs (BarrierSeal, BarrierRecoveryGenerate, ...) on the SO
// PIN. Without it those RPCs fail closed. The backend is decorated with the SO
// PIN lockout, so a raw backend is still throttled.
var WithRegisterPINBackend = qrdbgrpc.WithRegisterPINBackend

// WithRegisterCeremonyService attaches a ceremony service to the registered
// qrdb gRPC service.
var WithRegisterCeremonyService = qrdbgrpc.WithRegisterCeremonyService

// WithRegisterCredentialService attaches a credential service to the
// registered qrdb gRPC service.
var WithRegisterCredentialService = qrdbgrpc.WithRegisterCredentialService

// WithRegisterUserService attaches the user management service to the
// registered qrdb gRPC service (user lookup / CRUD on qrdb.v1.QRDBService).
var WithRegisterUserService = qrdbgrpc.WithRegisterUserService

// WithRegisterUserAuthFactorService attaches the user-side auth factor
// service. RegisterGRPCService registers it as the separate
// qrdb.v1.UserAuthFactorService on the same *grpc.Server.
var WithRegisterUserAuthFactorService = qrdbgrpc.WithRegisterUserAuthFactorService

// WithRegisterTenantBarrierService attaches the per-tenant barrier lifecycle
// service (a ServiceTenantServicer). RegisterGRPCService registers it as the
// separate qrdb.v1.TenantBarrierService on the same *grpc.Server, gated by
// the middleware from WithRegisterAuthMiddleware; a nil middleware is the
// no-auth posture.
var WithRegisterTenantBarrierService = qrdbgrpc.WithRegisterTenantBarrierService

// WithRegisterAuthMiddleware attaches the AuthMiddleware the registered
// services use for their handler-level tenancy gates (the certs surface and
// TenantBarrierService). It is the same instance the interceptors use; a
// composer that omits it gets the no-auth posture, in which the gates are
// no-ops.
var WithRegisterAuthMiddleware = qrdbgrpc.WithRegisterAuthMiddleware
