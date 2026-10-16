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

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
// operative while the barrier is sealed: the unseal call and the first-boot
// verbs. Each entry is a fully-qualified gRPC method path
// ("/qrdb.v1.QRDBService/BarrierUnseal"), matched case-sensitively against
// info.FullMethod in full. Methods not in this list -- Health and Status
// included -- return SealedError while the barrier is sealed.
//
// The list is derived from the canonical transport-neutral verb table in
// pkg/api/sealedwindow so it cannot drift from the other transports and is
// qualified with the QRDBService service name taken
// from the generated ServiceDesc. Matching on the full service-qualified path
// — rather than the bare method tail — means a same-named method on any OTHER
// co-registered gRPC service (e.g. "/qrdb.v1.TenantBarrierService/Status") is
// NOT admitted through the sealed window.
var SealedWindowAllowlist = sealedwindow.GRPCFullMethodAllowlist(pb.QRDBService_ServiceDesc.ServiceName)

// SealedWindowUnaryInterceptor returns a grpc.UnaryServerInterceptor
// that rejects every method NOT on the allowlist with SealedError while
// the supplied barrier is sealed. It has no enrollment probe, so it refuses
// the first-boot verbs too: in qrdb it sees only a cluster-held node, whose
// barrier is initialized. Once the barrier is unsealed the interceptor
// becomes a passthrough; on re-seal it engages again. The same
// interceptor instance is safe to keep installed across the
// lifetime of the process. It is the gate of a regular listener, whose
// own authentication interceptor decides every request it passes.
//
// allowlist may be nil, in which case SealedWindowAllowlist is
// used. Entries are fully-qualified gRPC method paths
// ("/qrdb.v1.QRDBService/BarrierUnseal") matched in full against info.FullMethod.
func SealedWindowUnaryInterceptor(barrier sealedWindowProbe, allowlist []string) grpc.UnaryServerInterceptor {
	return sealedWindowUnary(barrier, allowlist, nil)
}

// SealedWindowListenerUnaryInterceptor returns the unary gate of the
// boot-time sealed-window listener. While the barrier is sealed it admits
// only the allowlist, exactly as SealedWindowUnaryInterceptor does. Once
// the barrier is unsealed it refuses EVERY method with codes.Unavailable:
// that listener runs without authentication, so it must not serve anything
// against an unsealed barrier, even in the moment before it is stopped.
// While sealed it serves the first-boot verbs only while probe.EnrollmentOpen
// reports enrollment open, and refuses them with SealedError otherwise.
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

// SealedWindowDispatchUnaryInterceptor returns the unary interceptor of a
// regular listener that a re-sealed node shares with the boot-time
// sealed-window listener: a call that arrives while probe reports the barrier
// sealed runs sealed, the boot-time listener's chain, and every other call
// runs regular, the listener's own. The gate inside sealed samples probe
// again, so a call dispatched just before an unseal is refused rather than
// served anonymously against the unsealed barrier.
func SealedWindowDispatchUnaryInterceptor(probe sealedwindow.ListenerProbe, sealed, regular grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if probe.IsSealed() {
			return sealed(ctx, req, info, handler)
		}
		return regular(ctx, req, info, handler)
	}
}

// SealedWindowDispatchStreamInterceptor returns the streaming-RPC sibling of
// SealedWindowDispatchUnaryInterceptor.
func SealedWindowDispatchStreamInterceptor(probe sealedwindow.ListenerProbe, sealed, regular grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if probe.IsSealed() {
			return sealed(srv, ss, info, handler)
		}
		return regular(srv, ss, info, handler)
	}
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
	sealedwindow.RefuseSealed:      SealedError(),
	sealedwindow.RefuseUnconfirmed: mapServiceError(&clusterseal.SealStateUnconfirmedError{}),
	sealedwindow.RefuseClosed:      status.Error(codes.Unavailable, sealedwindow.ClosedMessage),
}

// SealedError returns the sealed refusal a sealed node answers every call
// but the unseal call with: codes.Unavailable carrying a google.rpc.ErrorInfo
// (domain sealedwindow.ErrorDomain, reason sealedwindow.ReasonSealed), so a
// client tells "up and sealed" apart from any other Unavailable without
// parsing the message.
func SealedError() error {
	st := status.New(codes.Unavailable, sealedwindow.SealedMessage)
	// WithDetails fails only for an OK status or a detail that cannot be
	// marshalled, and this is neither.
	detailed, _ := st.WithDetails(&errdetails.ErrorInfo{Domain: sealedwindow.ErrorDomain, Reason: sealedwindow.ReasonSealed})
	return detailed.Err()
}

// sealedWindowTransition holds the QRDBService methods of the Transition
// verbs (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.GRPCFullMethodTransition(pb.QRDBService_ServiceDesc.ServiceName)

// sealedWindowPreOperationalOnly holds the QRDBService methods of the
// PreOperationalOnly verbs: the first-boot verbs a sealed node serves only
// while enrollment is open.
var sealedWindowPreOperationalOnly = sealedwindow.GRPCFullMethodPreOperationalOnly(pb.QRDBService_ServiceDesc.ServiceName)

// sealedWindowAuthenticatedOnly holds the QRDBService methods of the
// AuthenticatedOnly verbs (cluster unseal), never served anonymously.
var sealedWindowAuthenticatedOnly = sealedwindow.GRPCFullMethodAuthenticatedOnly(pb.QRDBService_ServiceDesc.ServiceName)

// sealedWindowListenerOnly holds the QRDBService methods of the ListenerOnly
// verbs (barrier status), never served by a sealed regular listener.
var sealedWindowListenerOnly = sealedwindow.GRPCFullMethodListenerOnly(pb.QRDBService_ServiceDesc.ServiceName)

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
		ListenerOnly:      methodAllowed(fullMethod, sealedWindowListenerOnly),
	}
	if req.Sealed && req.Allowlisted && methodAllowed(fullMethod, sealedWindowPreOperationalOnly) {
		req.PreOperationalOnly = true
		req.EnrollmentOpen = listener != nil && listener.EnrollmentOpen(ctx)
	}
	return sealedWindowRefusals[sealedwindow.Decide(req)]
}

// methodAllowed checks whether the fully-qualified info.FullMethod
// ("/qrdb.v1.QRDBService/BarrierUnseal") is in the allowlist. The comparison is on
// the FULL service-qualified path, not the bare method tail, so a same-named
// method on a different co-registered gRPC service is not admitted.
func methodAllowed(fullMethod string, allowSet map[string]struct{}) bool {
	_, ok := allowSet[fullMethod]
	return ok
}
