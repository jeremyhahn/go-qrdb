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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// sealedWindowProbe is the abstraction the interceptor needs to know
// whether the storage barrier is currently sealed. Implemented by
// *seal.StorageBarrier in production; defined as an interface so
// tests can substitute a deterministic fake.
type sealedWindowProbe interface {
	IsSealed() bool
}

// SealedWindowAllowlist is the set of QRDBService methods that remain
// operative while the barrier is sealed. Each entry is a fully-qualified
// gRPC method path ("/qrdb.v1.QRDBService/Health"), matched case-sensitively
// against info.FullMethod in full. Methods not in this list return
// codes.Unavailable while the barrier is sealed.
//
// The list is derived from the canonical transport-neutral verb table in
// pkg/api/sealedwindow so it cannot drift from the other transports (every
// Barrier* and Ceremony* unseal-flow method, plus Health and Status for
// liveness probes) and is qualified with the QRDBService service name taken
// from the generated ServiceDesc. Matching on the full service-qualified path
// — rather than the bare method tail — means a same-named method on any OTHER
// co-registered gRPC service (e.g. "/qrdb.v1.TenantBarrierService/Status") is
// NOT admitted through the sealed window.
var SealedWindowAllowlist = sealedwindow.GRPCFullMethodAllowlist(pb.QRDBService_ServiceDesc.ServiceName)

// SealedWindowUnaryInterceptor returns a grpc.UnaryServerInterceptor
// that rejects every method NOT on the allowlist with
// codes.Unavailable + message "barrier sealed" while the supplied
// barrier is sealed. Once the barrier is unsealed the interceptor
// becomes a passthrough; on re-seal it engages again. The same
// interceptor instance is safe to keep installed across the
// lifetime of the process.
//
// allowlist may be nil, in which case SealedWindowAllowlist is
// used. Entries are fully-qualified gRPC method paths
// ("/qrdb.v1.QRDBService/Health") matched in full against info.FullMethod.
func SealedWindowUnaryInterceptor(barrier sealedWindowProbe, allowlist []string) grpc.UnaryServerInterceptor {
	if allowlist == nil {
		allowlist = SealedWindowAllowlist
	}
	allowSet := make(map[string]struct{}, len(allowlist))
	for _, m := range allowlist {
		allowSet[m] = struct{}{}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if barrier == nil || !barrier.IsSealed() {
			return handler(ctx, req)
		}
		if methodAllowed(info.FullMethod, allowSet) {
			return handler(ctx, req)
		}
		return nil, status.Error(codes.Unavailable, "barrier sealed")
	}
}

// SealedWindowStreamInterceptor returns the streaming-RPC sibling of
// SealedWindowUnaryInterceptor, applying the same allowlist
// semantics to streaming methods. Streaming methods are rejected
// with codes.Unavailable before the handler is invoked, so partial
// streams cannot leak past the seal boundary.
func SealedWindowStreamInterceptor(barrier sealedWindowProbe, allowlist []string) grpc.StreamServerInterceptor {
	if allowlist == nil {
		allowlist = SealedWindowAllowlist
	}
	allowSet := make(map[string]struct{}, len(allowlist))
	for _, m := range allowlist {
		allowSet[m] = struct{}{}
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if barrier == nil || !barrier.IsSealed() {
			return handler(srv, ss)
		}
		if methodAllowed(info.FullMethod, allowSet) {
			return handler(srv, ss)
		}
		return status.Error(codes.Unavailable, "barrier sealed")
	}
}

// methodAllowed checks whether the fully-qualified info.FullMethod
// ("/qrdb.v1.QRDBService/Health") is in the allowlist. The comparison is on
// the FULL service-qualified path, not the bare method tail, so a same-named
// method on a different co-registered gRPC service is not admitted.
func methodAllowed(fullMethod string, allowSet map[string]struct{}) bool {
	_, ok := allowSet[fullMethod]
	return ok
}
