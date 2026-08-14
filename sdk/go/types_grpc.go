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

package sdk

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

// WithRegisterBarrierService attaches a barrier service to the registered
// qrdb gRPC service.
var WithRegisterBarrierService = qrdbgrpc.WithRegisterBarrierService

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
