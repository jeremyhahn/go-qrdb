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
	"crypto"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// WithReplicaForwarding makes this listener forward a unary request it
// refuses because the node holds no replica of the database to a node that
// does, on the caller's behalf (docs/replica-forwarding). Without it the
// listener answers the refusal. Streaming methods are never forwarded.
func WithReplicaForwarding(cfg replicaforward.Config) ServerOption {
	return func(s *Server) {
		c := cfg
		s.replicaForwarding = &c
	}
}

// ReplicaForwarder forwards a gRPC request this node refused with
// NotAReplica to a replica, carrying a signed assertion of the caller, and
// relays the replica's answer. The TCP listener installs it through
// WithReplicaForwarding; the Unix socket listener, which serves the same
// services, installs it directly and forwards over gRPC.
type ReplicaForwarder struct {
	cfg       replicaforward.Config
	key       crypto.Signer
	nodeID    string
	clientTLS *tls.Config

	// conns holds one client connection per target endpoint. Connections
	// are opened on first use and closed with the listener.
	mu     sync.Mutex
	conns  map[string]*grpc.ClientConn
	closed bool
}

// NewReplicaForwarder returns the forwarder for a listener serving nodeTLS:
// the listener's own certificate is the client certificate a forward
// presents, and the key that signs the caller assertion.
func NewReplicaForwarder(cfg replicaforward.Config, nodeTLS *tls.Config) *ReplicaForwarder {
	key, nodeID := forward.NodeIdentity(nodeTLS)
	return &ReplicaForwarder{
		cfg:    cfg,
		key:    key,
		nodeID: nodeID,
		clientTLS: &tls.Config{
			RootCAs:              cfg.RootCAs,
			Certificates:         nodeTLS.Certificates,
			GetClientCertificate: nodeTLS.GetClientCertificate,
			MinVersion:           tls.VersionTLS13,
		},
		conns: map[string]*grpc.ClientConn{},
	}
}

// Close closes every target connection.
func (f *ReplicaForwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for ep, cc := range f.conns {
		_ = cc.Close()
		delete(f.conns, ep)
	}
}

// UnaryInterceptor returns the forwarding interceptor. It must sit
// innermost, after authentication and authorization, so the caller has been
// admitted here before anything is forwarded.
func (f *ReplicaForwarder) UnaryInterceptor() grpc.UnaryServerInterceptor { return f.unary }

// unary forwards what handler refuses as a non-replica to a replica, and
// what it refuses as not the leader to the leader that refusal names.
func (f *ReplicaForwarder) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	resp, err := handler(ctx, req)
	if err == nil || forwardedIn(ctx) {
		return resp, err
	}
	reason, md, ok := refusalFromStatus(status.Convert(err))
	if !ok {
		return resp, err
	}
	if reason == replicaforward.ReasonNotLeader {
		if reply, ferr, forwarded := f.forwardAdminWrite(ctx, info.FullMethod, req, replicaforward.LeaderRaftAddr(md)); forwarded {
			return reply, ferr
		}
		return resp, err
	}
	if reason != replicaforward.ReasonNotAReplica {
		return resp, err
	}
	refusal, derr := replicaforward.ParseNotAReplica(md)
	if derr != nil {
		return resp, err
	}
	return f.forward(ctx, info.FullMethod, req, refusal, err)
}

// forward forwards req and relays the answer. Every failure to forward ends
// in the refusal, never in a local answer; a failure after the request
// reached a target ends in that failure.
func (f *ReplicaForwarder) forward(ctx context.Context, fullMethod string, req any, refusal *placement.NotAReplicaError, refusalErr error) (any, error) {
	msg, ok := req.(proto.Message)
	if !ok {
		return nil, refusalErr
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil, refusalErr
	}
	newReply, ok := replyType(fullMethod)
	if !ok {
		return nil, refusalErr
	}
	assertion, sig, err := forward.SignCaller(auth.IdentityFromContext(ctx), forward.RPCBinding("grpc", fullMethod, payload), f.key, f.nodeID, time.Now())
	if err != nil {
		slog.Warn("replicaforward: not forwarded", "protocol", cluster.APIProtocolGRPC, "tenant", refusal.TenantID, "database", refusal.Database, "error", err)
		return nil, refusalErr
	}
	out := forwardedMetadata(ctx)
	out.Set(forward.MetadataAssertion, assertion)
	out.Set(forward.MetadataSignature, sig)

	var (
		reply     proto.Message
		answerErr error
		header    metadata.MD
		trailer   metadata.MD
	)
	send := func(ctx context.Context, t replicaforward.Target) (replicaforward.Result, error) {
		cc, ok := f.conn(ctx, t.Endpoint)
		if !ok {
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		reply, header, trailer = newReply(), nil, nil
		ierr := cc.Invoke(metadata.NewOutgoingContext(ctx, out), fullMethod, req, reply, grpc.Header(&header), grpc.Trailer(&trailer))
		if ierr == nil {
			answerErr = nil
			return replicaforward.Result{Outcome: replicaforward.Served}, nil
		}
		if len(header.Get(replicaforward.MetadataServedBy))+len(trailer.Get(replicaforward.MetadataServedBy)) == 0 {
			return replicaforward.Result{}, ierr
		}
		if reason, md, isRefusal := refusalFromStatus(status.Convert(ierr)); isRefusal {
			switch reason {
			case replicaforward.ReasonNotLeader:
				return replicaforward.Result{Outcome: replicaforward.NotLeader, LeaderRaftAddr: replicaforward.LeaderRaftAddr(md)}, nil
			case replicaforward.ReasonNotAReplica:
				return replicaforward.Result{Outcome: replicaforward.NotAReplica}, nil
			}
		}
		answerErr = ierr
		return replicaforward.Result{Outcome: replicaforward.Served}, nil
	}

	fctx, cancel := context.WithTimeout(ctx, f.cfg.ForwardTimeout())
	defer cancel()
	target, ferr := f.cfg.Locator.Forward(fctx, refusal, cluster.APIProtocolGRPC, send)
	if ferr != nil {
		var noTarget *replicaforward.NoTargetServedError
		var lookup *replicaforward.MemberLookupError
		var ownerErr *database.BindingOwnerUnavailableError
		if errors.As(ferr, &ownerErr) {
			return nil, mapServiceError(ferr)
		}
		if errors.As(ferr, &noTarget) || errors.As(ferr, &lookup) {
			return nil, refusalErr
		}
		return nil, status.Convert(ferr).Err()
	}
	relayMetadata(ctx, header, trailer, target.ReplicaID)
	if answerErr != nil {
		return nil, answerErr
	}
	return reply, nil
}

// forwardAdminWrite sends fullMethod/req, unchanged, to the leader at
// leaderAddr of the shard that refused it. Unlike forward it is not a
// placement-based retry across a replica set -- a leader is one node, not a
// set to retry across -- it is the single hop a leader-only write's own
// refusal (*service.NotLeaderError, service.AdminCredentialNotLeaderError)
// already names by address. It resolves that address through the SAME member records
// forward's Locator reads, signs the identical on-behalf assertion, and
// relays the leader's answer. ok is false whenever the write was not
// forwarded (no forwarder, unresolvable leader, or a transport failure
// before any request reached it), and the caller falls back to its own
// refusal, exactly as REST's tryForwardWrite does on the same conditions.
func (f *ReplicaForwarder) forwardAdminWrite(ctx context.Context, fullMethod string, req any, leaderAddr string) (any, error, bool) {
	if leaderAddr == "" {
		slog.Warn("admin forwarding: refusal names no leader", "method", fullMethod)
		return nil, nil, false
	}
	msg, ok := req.(proto.Message)
	if !ok {
		slog.Warn("admin forwarding: request is not a proto.Message", "method", fullMethod, "type", fmt.Sprintf("%T", req))
		return nil, nil, false
	}
	// The deadline covers the leader lookup too: it reads the member
	// records from the system shard, and a read that does not complete
	// would otherwise hold this call open for as long as the caller waits.
	fctx, cancel := context.WithTimeout(ctx, f.cfg.ForwardTimeout())
	defer cancel()
	member, found, err := f.cfg.Locator.MemberByAddress(fctx, leaderAddr)
	if err != nil || !found {
		slog.Warn("admin forwarding: leader not found in member records", "leader_raft_addr", leaderAddr, "error", err)
		return nil, nil, false
	}
	endpoint, ok := member.APIEndpoint(cluster.APIProtocolGRPC)
	if !ok || endpoint == "" {
		slog.Warn("admin forwarding: leader advertises no gRPC endpoint", "leader_raft_addr", leaderAddr)
		return nil, nil, false
	}
	newReply, ok := replyType(fullMethod)
	if !ok {
		slog.Warn("admin forwarding: method has no registered reply type", "method", fullMethod)
		return nil, nil, false
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		slog.Warn("admin forwarding: not forwarded", "leader_raft_addr", leaderAddr, "error", err)
		return nil, nil, false
	}
	assertion, sig, err := forward.SignCaller(auth.IdentityFromContext(ctx), forward.RPCBinding("grpc", fullMethod, payload), f.key, f.nodeID, time.Now())
	if err != nil {
		slog.Warn("admin forwarding: not forwarded", "leader_raft_addr", leaderAddr, "error", err)
		return nil, nil, false
	}
	out := forwardedMetadata(ctx)
	out.Set(forward.MetadataAssertion, assertion)
	out.Set(forward.MetadataSignature, sig)

	cc, ok := f.conn(fctx, endpoint)
	if !ok {
		slog.Warn("admin forwarding: could not connect to leader", "leader_raft_addr", leaderAddr, "endpoint", endpoint)
		return nil, nil, false
	}
	reply := newReply()
	var header, trailer metadata.MD
	ierr := cc.Invoke(metadata.NewOutgoingContext(fctx, out), fullMethod, req, reply, grpc.Header(&header), grpc.Trailer(&trailer))
	if ierr == nil {
		return reply, nil, true
	}
	// The served-by marker is set only once the leader's own handler ran:
	// its absence means the request never reached it (a dial/transport
	// failure), and the caller falls back to its own refusal, exactly as
	// forward does above for the per-database case. Its presence means the
	// leader answered -- with an error, here -- and that answer, not this
	// node's stale refusal, is relayed as the final result.
	if len(header.Get(replicaforward.MetadataServedBy))+len(trailer.Get(replicaforward.MetadataServedBy)) == 0 {
		slog.Warn("admin forwarding: could not reach leader", "leader_raft_addr", leaderAddr, "endpoint", endpoint, "error", ierr)
		return nil, nil, false
	}
	return nil, ierr, true
}

// conn returns a connection to endpoint that is ready to carry a request, or
// false when none could be opened within ctx: the request then never left
// this node, and the locator may try another replica.
func (f *ReplicaForwarder) conn(ctx context.Context, endpoint string) (*grpc.ClientConn, bool) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, false
	}
	cc, ok := f.conns[endpoint]
	if !ok {
		var err error
		cc, err = grpc.NewClient(endpoint,
			grpc.WithTransportCredentials(credentials.NewTLS(f.clientTLS)),
			grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{
				BaseDelay: replicaforward.ReconnectBaseDelay, Multiplier: replicaforward.ReconnectMultiplier,
				Jitter: replicaforward.ReconnectJitter, MaxDelay: replicaforward.ReconnectMaxDelay,
			}}),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(DefaultMaxMessageBytes), grpc.MaxCallSendMsgSize(DefaultMaxMessageBytes)),
		)
		if err != nil {
			f.mu.Unlock()
			return nil, false
		}
		f.conns[endpoint] = cc
	}
	f.mu.Unlock()
	return cc, awaitReady(ctx, cc, f.cfg.ConnectTimeout())
}

// awaitReady waits until cc is ready, for at most wait. A connection in
// transient failure is waited on through its reconnects, as a call that
// waits for ready is, so a target that is restarting, or that fails a
// handshake under load, is not counted unreachable at its first failure. A
// connection already in transient failure has its backoff reset first, so a
// target that came back is tried at once rather than after the backoff.
func awaitReady(ctx context.Context, cc *grpc.ClientConn, wait time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	cc.Connect()
	if cc.GetState() == connectivity.TransientFailure {
		cc.ResetConnectBackoff()
	}
	for {
		state := cc.GetState()
		switch state {
		case connectivity.Ready:
			return true
		case connectivity.Shutdown:
			return false
		case connectivity.Idle:
			cc.Connect()
		}
		if !cc.WaitForStateChange(ctx, state) {
			return false
		}
	}
}

// forwardedIn reports whether the request was forwarded here on a caller's
// behalf -- the hop marker. Such a request is never forwarded again. The
// auth interceptor has already verified the assertion it carries.
func forwardedIn(ctx context.Context) bool {
	_, _, ok := authmw.GRPCOnBehalfAssertion(ctx)
	return ok
}

// notForwardedKeys are the request metadata a forward does not carry: the
// caller's credentials (the target authenticates the assertion instead),
// the forward's own keys, and what the transport sets per connection.
var notForwardedKeys = map[string]bool{
	"authorization":                    true,
	forward.MetadataAssertion:          true,
	forward.MetadataSignature:          true,
	replicaforward.MetadataServedBy:    true,
	replicaforward.MetadataForwardedTo: true,
	":authority":                       true,
	"content-type":                     true,
	"user-agent":                       true,
	"te":                               true,
}

// forwardedMetadata is the request metadata a forward carries: the caller's,
// less notForwardedKeys and the transport's grpc- keys.
func forwardedMetadata(ctx context.Context) metadata.MD {
	in, _ := metadata.FromIncomingContext(ctx)
	out := metadata.MD{}
	for k, v := range in {
		if notForwardedKeys[k] || strings.HasPrefix(k, "grpc-") {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

// relayMetadata sets the target's header and trailer on this node's answer,
// less the served-by marker, and adds which replica served it.
func relayMetadata(ctx context.Context, header, trailer metadata.MD, replicaID uint64) {
	h := header.Copy()
	delete(h, replicaforward.MetadataServedBy)
	h.Set(replicaforward.MetadataForwardedTo, strconv.FormatUint(replicaID, 10))
	_ = grpc.SetHeader(ctx, h)
	t := trailer.Copy()
	delete(t, replicaforward.MetadataServedBy)
	if len(t) > 0 {
		_ = grpc.SetTrailer(ctx, t)
	}
}

// servedByUnary marks the answer to a request forwarded here on a caller's
// behalf as this node's own, so the forwarding node can tell it from a
// failure on the way. Every listener installs it, forwarding or not.
func servedByUnary(nodeID string) grpc.UnaryServerInterceptor {
	if nodeID == "" {
		nodeID = "unidentified"
	}
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if forwardedIn(ctx) {
			_ = grpc.SetTrailer(ctx, metadata.Pairs(replicaforward.MetadataServedBy, nodeID))
		}
		return handler(ctx, req)
	}
}

// replyType returns a constructor for fullMethod's response message, from
// the method's registered descriptor.
func replyType(fullMethod string) (func() proto.Message, bool) {
	name := strings.TrimPrefix(fullMethod, "/")
	i := strings.LastIndex(name, "/")
	if i <= 0 {
		return nil, false
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name[:i]))
	if err != nil {
		return nil, false
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, false
	}
	md := sd.Methods().ByName(protoreflect.Name(name[i+1:]))
	if md == nil || md.IsStreamingClient() || md.IsStreamingServer() {
		return nil, false
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Output().FullName())
	if err != nil {
		return nil, false
	}
	return func() proto.Message { return mt.New().Interface() }, true
}
