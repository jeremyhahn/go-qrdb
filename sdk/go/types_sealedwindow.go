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

import (
	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	quicapi "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	restapi "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbserver "github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// ---------------------------------------------------------------------------
// Sealed-window composition surface
//
// Re-exported so a composing service (e.g. go-quicbaas) can install the
// sealed-window gate on an embedded qrdb server — and mirror it onto its
// own transports — without importing pkg/server or pkg/api/* directly.
// ---------------------------------------------------------------------------

// SealedWindowProbe reports whether the storage barrier is currently
// sealed. *seal.StorageBarrier satisfies this directly via its IsSealed
// method; tests may substitute a deterministic fake.
type SealedWindowProbe = qrdbserver.SealedWindowProbe

// WithSealedWindowProbe installs the sealed-window middleware /
// interceptors on every protocol server of an embedded qrdb server.
// While the probe reports sealed, every endpoint not on the canonical
// allowlist returns 503 / Unavailable with the documented sealed
// envelope.
var WithSealedWindowProbe = qrdbserver.WithSealedWindowProbe

// AuthMiddleware is the qrdb authentication / authorization middleware
// chain shared by every protocol server.
type AuthMiddleware = authmw.AuthMiddleware

// WithAuthMiddleware configures authentication and authorization
// middleware for the protocol servers of an embedded qrdb server.
var WithAuthMiddleware = qrdbserver.WithAuthMiddleware

// ServeSealedWindow brings up an API-only sealed-window listener and
// blocks until the probe reports unsealed or ctx is canceled. See
// pkg/server.ServeSealedWindow; SDK node embedders normally use the
// higher-level node.ServeSealedWindow instead.
var ServeSealedWindow = qrdbserver.ServeSealedWindow

// SealedWindowCanceledError is returned by ServeSealedWindow when ctx is
// canceled before the operator drives the barrier unsealed.
type SealedWindowCanceledError = qrdbserver.SealedWindowCanceledError

// ---------------------------------------------------------------------------
// Per-transport sealed-window allowlists
//
// All four lists are derived from the canonical transport-neutral verb
// table in pkg/api/sealedwindow, so they cannot drift apart.
// ---------------------------------------------------------------------------

// RESTSealedWindowAllowlist is the REST sealed-window allowlist. Entries
// ending in "/" are prefix matches; everything else matches exactly.
var RESTSealedWindowAllowlist = restapi.SealedWindowAllowlist

// GRPCSealedWindowAllowlist is the gRPC sealed-window allowlist. Entries are
// fully service-qualified gRPC methods (the complete FullMethod path, e.g.
// "/qrdb.v1.QRDBService/BarrierRekey") — NOT bare method tails. Matching the
// full path prevents a same-named method on a co-registered service from being
// admitted while the barrier is sealed.
var GRPCSealedWindowAllowlist = grpcapi.SealedWindowAllowlist

// QUICSealedWindowAllowlist is the native QUIC sealed-window allowlist,
// keyed by method name (e.g. "barrier.Unseal").
var QUICSealedWindowAllowlist = quicapi.SealedWindowAllowlist

// MCPSealedWindowAllowlist is the MCP sealed-window allowlist, keyed by
// tool name (e.g. "qrdb_barrier_unseal").
var MCPSealedWindowAllowlist = mcpapi.SealedWindowAllowlist

// SealedWindowRESTAllowlist derives a fresh REST allowlist from the
// canonical verb table (useful for composing with project-specific
// extensions without mutating the shared package var).
var SealedWindowRESTAllowlist = sealedwindow.RESTAllowlist

// SealedWindowMiddleware returns an http middleware that 503s every
// request not on the allowlist while the supplied barrier probe reports
// sealed. Pass nil for the allowlist to use the canonical REST list.
var SealedWindowMiddleware = restapi.SealedWindowMiddleware

// SealedWindowUnaryInterceptor returns a grpc.UnaryServerInterceptor
// that rejects every method not on the allowlist with codes.Unavailable
// while the supplied barrier probe reports sealed. Pass nil for the
// allowlist to use the canonical gRPC list.
var SealedWindowUnaryInterceptor = grpcapi.SealedWindowUnaryInterceptor

// SealedWindowStreamInterceptor is the streaming-RPC sibling of
// SealedWindowUnaryInterceptor.
var SealedWindowStreamInterceptor = grpcapi.SealedWindowStreamInterceptor

// SealedWindowListenerProbe is what a boot-time sealed-window listener's
// gate reads per request: the barrier's sealed state and whether anonymous
// officer enrollment is still open.
type SealedWindowListenerProbe = sealedwindow.ListenerProbe

// SealedWindowListenerMiddleware is the REST gate of a boot-time
// sealed-window listener, a listener with no authentication behind it: the
// allowlist while the barrier probe reports sealed, and a 503 for every
// request once it reports unsealed.
var SealedWindowListenerMiddleware = restapi.SealedWindowListenerMiddleware

// SealedWindowListenerUnaryInterceptor is the gRPC unary gate of a
// boot-time sealed-window listener: the allowlist while sealed, and
// codes.Unavailable for every method once unsealed.
var SealedWindowListenerUnaryInterceptor = grpcapi.SealedWindowListenerUnaryInterceptor

// SealedWindowListenerStreamInterceptor is the streaming-RPC sibling of
// SealedWindowListenerUnaryInterceptor.
var SealedWindowListenerStreamInterceptor = grpcapi.SealedWindowListenerStreamInterceptor

// ---------------------------------------------------------------------------
// Seal-only service graph (pre-host sealed window)
// ---------------------------------------------------------------------------

// LifecycleInfra carries the infrastructure dependencies consumed by the
// lifecycle service builders (BuildSealServicesOnly and the full
// BuildServices graph).
type LifecycleInfra = lifecycle.Infra

// LifecycleNode aggregates the service instances produced by the
// lifecycle builders. node.Node.Services() returns this type.
type LifecycleNode = lifecycle.Node

// BuildSealServicesOnly constructs the subset of seal-gated services
// that do not require a Raft host: Barrier, Credential, Ceremony, and
// (when a BarrierRegistry is supplied) Tenant. Intended for the pre-host
// sealed-window boot phase.
var BuildSealServicesOnly = lifecycle.BuildSealServicesOnly
