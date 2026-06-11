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
)

// sealedWindowProbe is the abstraction the interceptor needs to know
// whether the storage barrier is currently sealed. Implemented by
// *seal.StorageBarrier in production; defined as an interface so
// tests can substitute a deterministic fake.
type sealedWindowProbe interface {
	IsSealed() bool
}

// SealedWindowAllowlist is the canonical set of QRDBService method
// names that remain operative while the barrier is sealed. Each
// entry is the trailing component of the gRPC method (the suffix
// after the last "/"), matched case-sensitively against
// info.FullMethod's tail. Methods not in this list return
// codes.Unavailable while the barrier is sealed.
//
// All Barrier* and Ceremony* methods are included here so operators
// can drive the unseal flow over gRPC. Health and Status are also
// allowlisted for liveness probes.
var SealedWindowAllowlist = []string{
	"Health",
	"Status",
	"BarrierStatus",
	"BarrierInitialize",
	"BarrierUnseal",
	"BarrierShamirInitialize",
	"BarrierShamirUnsealShare",
	"BarrierShamirUnsealShares",
	"BarrierRekey",
	"BarrierRecoveryGenerate",
	"BarrierRecoveryRecover",
	"BarrierRecoveryDelete",
	"BarrierRecoveryStatus",
	"BarrierRootToken",
	"BarrierOfficerRegister",
	"BarrierOfficerList",
	"BarrierOfficerClaimStatus",
	"BarrierShareClaimBegin",
	"BarrierShareClaimComplete",
	"BarrierRecoveryOfficerRegister",
	"BarrierRecoveryShareClaimBegin",
	"BarrierRecoveryShareClaimComplete",
	"CeremonyInitialize",
	"CeremonyStatus",
	"CeremonyClaimCertBegin",
	"CeremonyClaimCertComplete",
	"CeremonyClaimShare",
	"CeremonySignCSR",
	"CeremonyEnrollExternalOfficer",
}

// SealedWindowUnaryInterceptor returns a grpc.UnaryServerInterceptor
// that rejects every method NOT on the allowlist with
// codes.Unavailable + message "barrier sealed" while the supplied
// barrier is sealed. Once the barrier is unsealed the interceptor
// becomes a passthrough; on re-seal it engages again. The same
// interceptor instance is safe to keep installed across the
// lifetime of the process.
//
// allowlist may be nil, in which case SealedWindowAllowlist is
// used. Methods are matched by their trailing component (everything
// after the last "/" in info.FullMethod).
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

// methodAllowed checks whether the trailing component of
// info.FullMethod (the part after the final "/") is in the
// allowlist.
func methodAllowed(fullMethod string, allowSet map[string]struct{}) bool {
	tail := fullMethod
	for i := len(fullMethod) - 1; i >= 0; i-- {
		if fullMethod[i] == '/' {
			tail = fullMethod[i+1:]
			break
		}
	}
	_, ok := allowSet[tail]
	return ok
}
