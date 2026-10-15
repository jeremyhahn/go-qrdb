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
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
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
// lifetime of the process. It is the gate of a regular listener, whose
// own authentication interceptor decides every request it passes.
//
// allowlist may be nil, in which case SealedWindowAllowlist is
// used. Entries are fully-qualified gRPC method paths
// ("/qrdb.v1.QRDBService/Health") matched in full against info.FullMethod.
func SealedWindowUnaryInterceptor(barrier sealedWindowProbe, allowlist []string) grpc.UnaryServerInterceptor {
	return sealedWindowUnary(barrier, allowlist, nil)
}

// SealedWindowListenerUnaryInterceptor returns the unary gate of the
// boot-time sealed-window listener. While the barrier is sealed it admits
// only the allowlist, exactly as SealedWindowUnaryInterceptor does. Once
// the barrier is unsealed it refuses EVERY method with codes.Unavailable:
// that listener runs without authentication, so it must not serve anything
// against an unsealed barrier, even in the moment before it is stopped.
// While sealed it also refuses officer and recovery-officer registration with
// codes.PermissionDenied once probe.EnrollmentOpen reports enrollment closed.
// The probe is sampled on every call; a nil probe refuses every call.
func SealedWindowListenerUnaryInterceptor(probe sealedwindow.ListenerProbe, allowlist []string) grpc.UnaryServerInterceptor {
	probe = sealedwindow.OrFailClosed(probe)
	return sealedWindowUnary(probe, allowlist, probe)
}

// SealedWindowStreamInterceptor returns the streaming-RPC sibling of
// SealedWindowUnaryInterceptor, applying the same allowlist
// semantics to streaming methods. Streaming methods are rejected
// with codes.Unavailable before the handler is invoked, so partial
// streams cannot leak past the seal boundary.
func SealedWindowStreamInterceptor(barrier sealedWindowProbe, allowlist []string) grpc.StreamServerInterceptor {
	return sealedWindowStream(barrier, allowlist, nil)
}

// SealedWindowListenerStreamInterceptor returns the streaming-RPC sibling
// of SealedWindowListenerUnaryInterceptor.
func SealedWindowListenerStreamInterceptor(probe sealedwindow.ListenerProbe, allowlist []string) grpc.StreamServerInterceptor {
	probe = sealedwindow.OrFailClosed(probe)
	return sealedWindowStream(probe, allowlist, probe)
}

// sealedWindowUnary applies sealedwindow.Decide to every unary call. listener
// is nil for a regular listener's gate and the boot-time sealed-window
// listener's probe otherwise.
func sealedWindowUnary(barrier sealedWindowProbe, allowlist []string, listener sealedwindow.ListenerProbe) grpc.UnaryServerInterceptor {
	allowSet := sealedWindowAllowSet(allowlist)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if listener != nil {
			// Held until the call completes; see sealedwindow.Admission.
			defer listener.Admit(methodAllowed(info.FullMethod, sealedWindowTransition))()
		}
		if err := sealedWindowAdmit(ctx, barrier, info.FullMethod, allowSet, listener); err != nil {
			return nil, err
		}
		if listener != nil {
			ctx = sealedwindow.WithAnonymous(ctx)
		}
		return handler(ctx, req)
	}
}

// sealedWindowStream applies sealedwindow.Decide to every stream.
func sealedWindowStream(barrier sealedWindowProbe, allowlist []string, listener sealedwindow.ListenerProbe) grpc.StreamServerInterceptor {
	allowSet := sealedWindowAllowSet(allowlist)
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if listener != nil {
			// Held until the stream completes; see sealedwindow.Admission.
			defer listener.Admit(methodAllowed(info.FullMethod, sealedWindowTransition))()
		}
		// The stream's context is read only on the listener, the one gate
		// that consults it.
		ctx := context.Background()
		if listener != nil {
			ctx = ss.Context()
		}
		if err := sealedWindowAdmit(ctx, barrier, info.FullMethod, allowSet, listener); err != nil {
			return err
		}
		if listener != nil {
			ss = anonymousServerStream{ServerStream: ss, ctx: sealedwindow.WithAnonymous(ss.Context())}
		}
		return handler(srv, ss)
	}
}

// anonymousServerStream is a stream admitted by the boot-time sealed-window
// listener, whose context carries the sealedwindow.WithAnonymous mark.
type anonymousServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the marked context.
func (s anonymousServerStream) Context() context.Context { return s.ctx }

// sealedWindowAllowSet indexes allowlist, defaulting to
// SealedWindowAllowlist when it is nil.
func sealedWindowAllowSet(allowlist []string) map[string]struct{} {
	if allowlist == nil {
		allowlist = SealedWindowAllowlist
	}
	allowSet := make(map[string]struct{}, len(allowlist))
	for _, m := range allowlist {
		allowSet[m] = struct{}{}
	}
	return allowSet
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its
// status. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]error{
	sealedwindow.RefuseSealed:           status.Error(codes.Unavailable, "barrier sealed"),
	sealedwindow.RefuseUnconfirmed:      mapServiceError(&clusterseal.SealStateUnconfirmedError{}),
	sealedwindow.RefuseClosed:           status.Error(codes.Unavailable, sealedwindow.ClosedMessage),
	sealedwindow.RefuseEnrollmentClosed: status.Error(codes.PermissionDenied, sealedwindow.EnrollmentClosedMessage),
}

// sealedWindowTransition holds the QRDBService methods of the Transition
// verbs (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.GRPCFullMethodTransition(pb.QRDBService_ServiceDesc.ServiceName)

// sealedWindowPreOperationalOnly holds the QRDBService methods of the
// PreOperationalOnly verbs (officer and recovery-officer registration).
var sealedWindowPreOperationalOnly = sealedwindow.GRPCFullMethodPreOperationalOnly(pb.QRDBService_ServiceDesc.ServiceName)

// sealedWindowAuthenticatedOnly holds the QRDBService methods of the
// AuthenticatedOnly verbs (cluster unseal), never served anonymously.
var sealedWindowAuthenticatedOnly = sealedwindow.GRPCFullMethodAuthenticatedOnly(pb.QRDBService_ServiceDesc.ServiceName)

// sealedWindowAdmit returns nil when the gate admits fullMethod, and the
// refusal status otherwise.
func sealedWindowAdmit(ctx context.Context, barrier sealedWindowProbe, fullMethod string, allowSet map[string]struct{}, listener sealedwindow.ListenerProbe) error {
	sealed, unconfirmed := sealedwindow.Sample(barrier)
	req := sealedwindow.Request{
		Sealed:            sealed,
		Unconfirmed:       unconfirmed,
		Allowlisted:       methodAllowed(fullMethod, allowSet),
		Listener:          listener != nil,
		AuthenticatedOnly: methodAllowed(fullMethod, sealedWindowAuthenticatedOnly),
	}
	if req.Listener && req.Sealed && req.Allowlisted && methodAllowed(fullMethod, sealedWindowPreOperationalOnly) {
		req.PreOperationalOnly = true
		req.EnrollmentOpen = listener.EnrollmentOpen(ctx)
	}
	return sealedWindowRefusals[sealedwindow.Decide(req)]
}

// methodAllowed checks whether the fully-qualified info.FullMethod
// ("/qrdb.v1.QRDBService/Health") is in the allowlist. The comparison is on
// the FULL service-qualified path, not the bare method tail, so a same-named
// method on a different co-registered gRPC service is not admitted.
func methodAllowed(fullMethod string, allowSet map[string]struct{}) bool {
	_, ok := allowSet[fullMethod]
	return ok
}
