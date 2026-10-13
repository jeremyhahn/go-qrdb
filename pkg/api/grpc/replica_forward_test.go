// Copyright (c) 2026 Jeremy Hahn
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

package grpc

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// seenCall is what a fake target saw of one forwarded request.
type seenCall struct {
	subject, tenant, database string
	authorization             []string
	verifyErr                 error
}

// fakeTarget is a replica's gRPC listener. It verifies the on-behalf
// assertion exactly as the auth interceptor binds it -- the method and the
// deterministically serialized request, presented by a cluster peer -- and
// marks its answers served unless told it is something on the way.
type fakeTarget struct {
	pb.UnimplementedQRDBServiceServer
	addr     string
	raftAddr string
	calls    atomic.Int32
	answer   func(ctx context.Context) error
	unmarked bool

	mu   sync.Mutex
	seen []seenCall
}

func (f *fakeTarget) Put(ctx context.Context, _ *pb.PutRequest) (*pb.PutResponse, error) {
	if f.answer != nil {
		if err := f.answer(ctx); err != nil {
			return nil, err
		}
	}
	return &pb.PutResponse{}, nil
}

func (f *fakeTarget) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if f.answer != nil {
		if err := f.answer(ctx); err != nil {
			return nil, err
		}
	}
	return &pb.GetResponse{Value: []byte("value-of-" + req.GetKey())}, nil
}

func (f *fakeTarget) CeremonyInitialize(ctx context.Context, _ *pb.CeremonyInitializeRequest) (*pb.CeremonyInitializeResponse, error) {
	f.calls.Add(1)
	if f.answer != nil {
		if err := f.answer(ctx); err != nil {
			return nil, err
		}
	}
	return &pb.CeremonyInitializeResponse{SpkiPin: "leader-answered"}, nil
}

// RemoveMember answers as the system shard's leader would: it is where a
// follower's NotLeader refusal of a membership change must end up.
func (f *fakeTarget) RemoveMember(ctx context.Context, _ *pb.RemoveMemberRequest) (*emptypb.Empty, error) {
	if f.answer != nil {
		if err := f.answer(ctx); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

func (f *fakeTarget) record(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	f.calls.Add(1)
	call := seenCall{}
	md, _ := metadata.FromIncomingContext(ctx)
	call.authorization = md.Get("authorization")
	if v := md.Get(MetadataDatabase); len(v) > 0 {
		call.database = v[0]
	}
	assertion, sig, ok := grpcOnBehalfFromMD(md)
	p, _ := peer.FromContext(ctx)
	tlsInfo, _ := p.AuthInfo.(credentials.TLSInfo)
	payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(req.(proto.Message))
	if ok && len(tlsInfo.State.PeerCertificates) > 0 {
		a, err := forward.VerifyBinding(assertion, sig, tlsInfo.State.PeerCertificates[0], "", time.Now(),
			forward.RPCBinding("grpc", info.FullMethod, payload))
		call.verifyErr = err
		if a != nil {
			call.subject, call.tenant = a.Subject, a.TenantID
		}
	}
	f.mu.Lock()
	f.seen = append(f.seen, call)
	f.mu.Unlock()
	if call.verifyErr != nil || !ok {
		return nil, status.Error(codes.Unauthenticated, "no valid assertion")
	}
	return handler(ctx, req)
}

func (f *fakeTarget) lastSeen(t *testing.T) seenCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		t.Fatal("the target saw no request")
	}
	return f.seen[len(f.seen)-1]
}

func grpcOnBehalfFromMD(md metadata.MD) (string, string, bool) {
	a, s := md.Get(forward.MetadataAssertion), md.Get(forward.MetadataSignature)
	if len(a) == 0 || len(s) == 0 {
		return "", "", false
	}
	return a[0], s[0], true
}

// serveGRPC serves svc on 127.0.0.1 with tlsCfg and the chain, and returns
// the address.
func serveGRPC(t *testing.T, tlsCfg *tls.Config, svc pb.QRDBServiceServer, chain ...grpc.UnaryServerInterceptor) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)), grpc.ChainUnaryInterceptor(chain...))
	pb.RegisterQRDBServiceServer(srv, svc)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

func startTarget(t *testing.T, ca *replicaforwardtest.CA, nodeID, raftAddr string) *fakeTarget {
	t.Helper()
	return startTargetTLS(t, ca.TLS(t, nodeID), nodeID, raftAddr)
}

// startTargetTLS starts a target serving tlsCfg.
func startTargetTLS(t *testing.T, tlsCfg *tls.Config, nodeID, raftAddr string) *fakeTarget {
	t.Helper()
	ft := &fakeTarget{raftAddr: raftAddr}
	served := servedByUnary(nodeID)
	ft.addr = serveGRPC(t, tlsCfg, ft, ft.record,
		func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if ft.unmarked {
				return h(ctx, req)
			}
			return served(ctx, req, info, h)
		})
	return ft
}

// refusingService is the ingress node: it holds no replica, so every data
// method refuses with the placement's replica set.
type refusingService struct {
	pb.UnimplementedQRDBServiceServer
	refusal *placement.NotAReplicaError
	calls   atomic.Int32
}

func (r *refusingService) Put(context.Context, *pb.PutRequest) (*pb.PutResponse, error) {
	r.calls.Add(1)
	return nil, mapServiceError(r.refusal)
}

func (r *refusingService) Get(context.Context, *pb.GetRequest) (*pb.GetResponse, error) {
	r.calls.Add(1)
	return nil, mapServiceError(r.refusal)
}

// fwdCluster is an ingress node holding no replica and the replicas the
// placement names.
type fwdCluster struct {
	ca        *replicaforwardtest.CA
	targets   []*fakeTarget
	cache     *replicaforward.LeaderCache
	forwarder *ReplicaForwarder
	ingress   *refusingService
	client    pb.QRDBServiceClient
}

// newFwdCluster starts the ingress node, issued by ca, in front of the
// targets, which the placement names in order.
func newFwdCluster(t *testing.T, ca *replicaforwardtest.CA, timeout time.Duration, targets ...*fakeTarget) *fwdCluster {
	t.Helper()
	c := &fwdCluster{ca: ca, targets: targets, cache: replicaforward.NewLeaderCache()}
	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-a", LeaderReplicaID: 1}
	var members replicaforwardtest.Members
	for i, ft := range targets {
		refusal.Replicas = append(refusal.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: "node-" + strconv.Itoa(i+1), RaftAddr: ft.raftAddr})
		members = append(members, cluster.Member{NodeID: uint64(i + 1), Address: ft.raftAddr,
			APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: ft.addr}})
	}
	ingressTLS := c.ca.TLS(t, "node-a")
	c.forwarder = NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil),
		RootCAs: c.ca.Pool,
		Timeout: timeout,
	}, ingressTLS)
	t.Cleanup(c.forwarder.Close)
	c.ingress = &refusingService{refusal: refusal}
	authn := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if v := metadata.ValueFromIncomingContext(ctx, "x-test-anonymous"); len(v) == 0 {
			ctx = auth.WithIdentity(ctx, &auth.Identity{Subject: "alice", TenantID: "acme"})
		}
		return h(ctx, req)
	}
	addr := serveGRPC(t, ingressTLS, c.ingress, authn, servedByUnary("node-a"), c.forwarder.UnaryInterceptor())
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(c.ca.TLS(t, ""))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	c.client = pb.NewQRDBServiceClient(cc)
	return c
}

// awaitConnected opens the forwarder's connection to t and waits until it is
// ready. A forward gives the handshake half its budget and, when the
// connection is not ready by then, treats the target as unreachable -- the
// request never left -- and moves to the next replica. A test whose
// assertion is about what happens after the request reached t awaits the
// connection first, so a slow handshake on a loaded host cannot turn t into
// an unreachable target and the test into one about a different path.
func (c *fwdCluster) awaitConnected(t *testing.T, target *fakeTarget) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cc, _ := c.forwarder.conn(ctx, target.addr)
	if cc == nil {
		t.Fatalf("the forwarder opened no connection to %s", target.addr)
	}
	for state := cc.GetState(); state != connectivity.Ready; state = cc.GetState() {
		cc.Connect()
		if !cc.WaitForStateChange(ctx, state) {
			t.Fatalf("the forwarder's connection to %s never became ready (%s)", target.addr, state)
		}
	}
}

func callerCtx(pairs ...string) context.Context {
	md := metadata.Pairs(append([]string{MetadataDatabase, "orders", "authorization", "Bearer secret"}, pairs...)...)
	return metadata.NewOutgoingContext(context.Background(), md)
}

func TestReplicaForwardGRPC_ForwardsAsTheCallerAndRelaysTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startTarget(t, ca, "node-1", "raft-b")
	c := newFwdCluster(t, ca, 5*time.Second, b)

	var header metadata.MD
	resp, err := c.client.Get(callerCtx(), &pb.GetRequest{Key: "k1"}, grpc.Header(&header))
	if err != nil {
		t.Fatalf("Get through the non-replica = %v; want the replica's answer", err)
	}
	if string(resp.GetValue()) != "value-of-k1" {
		t.Fatalf("value = %q; want the replica's", resp.GetValue())
	}
	seen := b.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" || seen.tenant != "acme" {
		t.Fatalf("target saw %+v; want a verified assertion for alice/acme", seen)
	}
	if seen.database != "orders" || len(seen.authorization) != 0 {
		t.Fatalf("target saw database %q, authorization %v; want the scope carried and the credential dropped", seen.database, seen.authorization)
	}
	if got := header.Get(replicaforward.MetadataForwardedTo); len(got) != 1 || got[0] != "1" {
		t.Fatalf("%s = %v; want replica 1", replicaforward.MetadataForwardedTo, got)
	}
	if got := header.Get(replicaforward.MetadataServedBy); len(got) != 0 {
		t.Fatalf("the served-by marker reached the caller: %v", got)
	}
	if l, ok := c.cache.Get(42); !ok || l.RaftAddr != "raft-b" {
		t.Fatalf("cache = %+v, %v; want the replica that served", l, ok)
	}
}

func TestReplicaForwardGRPC_AForwardedRequestIsNeverForwardedAgain(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startTarget(t, ca, "node-1", "raft-b")
	c := newFwdCluster(t, ca, 5*time.Second, b)

	_, err := c.client.Put(callerCtx(forward.MetadataAssertion, "a", forward.MetadataSignature, "s"), &pb.PutRequest{Key: "k", Value: []byte("v")})
	if reason, _, ok := refusalFromStatus(status.Convert(err)); !ok || reason != replicaforward.ReasonNotAReplica {
		t.Fatalf("a forwarded request = %v; want the refusal", err)
	}
	if n := b.calls.Load(); n != 0 {
		t.Fatalf("a forwarded request was forwarded again (%d target calls)", n)
	}
}

func TestReplicaForwardGRPC_NotLeaderRetriesTheNamedLeader(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startTarget(t, ca, "node-1", "raft-b")
	cc := startTarget(t, ca, "node-2", "raft-c")
	b.answer = func(context.Context) error {
		return mapServiceError(&service.NotLeaderError{ShardID: 42, LeaderID: 2, LeaderAddress: "raft-c"})
	}
	c := newFwdCluster(t, ca, 5*time.Second, b, cc)

	var header metadata.MD
	if _, err := c.client.Put(callerCtx(), &pb.PutRequest{Key: "k", Value: []byte("v")}, grpc.Header(&header)); err != nil {
		t.Fatalf("Put = %v; want the named leader to serve it", err)
	}
	if b.calls.Load() != 1 || cc.calls.Load() != 1 {
		t.Fatalf("calls b=%d c=%d; want one each", b.calls.Load(), cc.calls.Load())
	}
	if got := header.Get(replicaforward.MetadataForwardedTo); len(got) != 1 || got[0] != "2" {
		t.Fatalf("%s = %v; want replica 2", replicaforward.MetadataForwardedTo, got)
	}
	if l, ok := c.cache.Get(42); !ok || l.RaftAddr != "raft-c" {
		t.Fatalf("cache = %+v, %v; want the named leader", l, ok)
	}
}

func TestReplicaForwardGRPC_UnreachableRetriesTheNextReplica(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	dead := &fakeTarget{raftAddr: "raft-b", addr: replicaforwardtest.ClosedAddr(t)}
	cc := startTarget(t, ca, "node-2", "raft-c")
	c := newFwdCluster(t, ca, 5*time.Second, dead, cc)

	if _, err := c.client.Put(callerCtx(), &pb.PutRequest{Key: "k", Value: []byte("v")}); err != nil {
		t.Fatalf("Put = %v; want the next replica to serve it", err)
	}
	if cc.calls.Load() != 1 {
		t.Fatalf("next replica calls = %d; want 1", cc.calls.Load())
	}
}

func TestReplicaForwardGRPC_AFailureAfterSendingIsNotRetried(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, b *fakeTarget){
		// Something between the nodes failed the request: the target's
		// handler never marked it served.
		"unmarked failure": func(_ *testing.T, b *fakeTarget) {
			b.unmarked = true
			b.answer = func(context.Context) error { return status.Error(codes.Internal, "on the way") }
		},
		// The target took the request and did not answer in time. It holds
		// the request until the subtest ends rather than until its own
		// deadline: that deadline is the forward's, and a target answering
		// on it races the forwarder's -- the answer, marked served, is the
		// target's own error, which caches the target as a replica that
		// answered.
		"timeout": func(t *testing.T, b *fakeTarget) {
			hold := make(chan struct{})
			t.Cleanup(func() { close(hold) })
			b.answer = func(context.Context) error { <-hold; return status.Error(codes.Internal, "too late") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca := replicaforwardtest.NewCA(t)
			b := startTarget(t, ca, "node-1", "raft-b")
			cc := startTarget(t, ca, "node-2", "raft-c")
			setup(t, b)
			c := newFwdCluster(t, ca, 300*time.Millisecond, b, cc)
			c.cache.Put(42, replicaforward.CachedLeader{TenantID: "acme", Database: "orders", ReplicaID: 1, RaftAddr: "raft-b",
				Endpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: b.addr}})
			c.awaitConnected(t, b)

			_, err := c.client.Put(callerCtx(), &pb.PutRequest{Key: "k", Value: []byte("v")})
			if err == nil {
				t.Fatal("Put succeeded; want the failure")
			}
			if _, _, isRefusal := refusalFromStatus(status.Convert(err)); isRefusal {
				t.Fatalf("Put = %v; want the failure, not the refusal", err)
			}
			if cc.calls.Load() != 0 {
				t.Fatalf("a request that may have applied was sent again (%d)", cc.calls.Load())
			}
			if _, ok := c.cache.Get(42); ok {
				t.Fatal("the cache kept a leader the forward failed at")
			}
		})
	}
}

func TestReplicaForwardGRPC_TheTargetsErrorIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startTarget(t, ca, "node-1", "raft-b")
	b.answer = func(context.Context) error { return mapServiceError(&service.KeyNotFoundError{Key: "k"}) }
	c := newFwdCluster(t, ca, 5*time.Second, b)

	_, err := c.client.Get(callerCtx(), &pb.GetRequest{Key: "k"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Get = %v; want the replica's NotFound", err)
	}
	if _, ok := c.cache.Get(42); !ok {
		t.Fatal("a replica that answered was not cached")
	}
}

// TestReplicaForwardGRPC_AReconnectingReplicaIsWaitedOn: the only replica
// refuses its first handshakes, as one restarting or overloaded does, so the
// forwarder's connection passes through transient failure before it is
// ready. The replica is waited on, and its answer is the answer -- not the
// forwarder's own refusal.
func TestReplicaForwardGRPC_AReconnectingReplicaIsWaitedOn(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	tlsCfg, offered := replicaforwardtest.RefuseHandshakes(ca.TLS(t, "node-1"), 2)
	b := startTargetTLS(t, tlsCfg, "node-1", "raft-b")
	b.answer = func(context.Context) error { return mapServiceError(&service.KeyNotFoundError{Key: "k"}) }
	c := newFwdCluster(t, ca, 5*time.Second, b)

	_, err := c.client.Get(callerCtx(), &pb.GetRequest{Key: "k"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Get = %v; want the reconnecting replica's NotFound", err)
	}
	if n := offered.Load(); n != 3 {
		t.Fatalf("handshakes offered = %d; want two refused, then the one that served", n)
	}
	if b.calls.Load() != 1 {
		t.Fatalf("replica calls = %d; want 1", b.calls.Load())
	}
}

func TestReplicaForwardGRPC_RefusesWhatItCannotForward(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx    context.Context
		answer func(context.Context) error
	}{
		"no authenticated caller": {ctx: callerCtx("x-test-anonymous", "1")},
		"every replica refuses": {ctx: callerCtx(), answer: func(context.Context) error {
			return mapServiceError(&placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42})
		}},
	} {
		t.Run(name, func(t *testing.T) {
			ca := replicaforwardtest.NewCA(t)
			b := startTarget(t, ca, "node-1", "raft-b")
			cc := startTarget(t, ca, "node-2", "raft-c")
			b.answer, cc.answer = tc.answer, tc.answer
			c := newFwdCluster(t, ca, 5*time.Second, b, cc)

			_, err := c.client.Put(tc.ctx, &pb.PutRequest{Key: "k", Value: []byte("v")})
			reason, md, ok := refusalFromStatus(status.Convert(err))
			if !ok || reason != replicaforward.ReasonNotAReplica || status.Code(err) != codes.Unavailable {
				t.Fatalf("Put = %v; want the ingress node's refusal", err)
			}
			if got, derr := replicaforward.ParseNotAReplica(md); derr != nil || got.NodeID != "node-a" || len(got.Replicas) != 2 {
				t.Fatalf("refusal = %+v, %v; want the ingress node's, with its replica set", got, derr)
			}
		})
	}
}

func TestReplicaForwardGRPC_AClosedForwarderRefuses(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startTarget(t, ca, "node-1", "raft-b")
	c := newFwdCluster(t, ca, 5*time.Second, b)
	c.forwarder.Close()

	_, err := c.client.Put(callerCtx(), &pb.PutRequest{Key: "k", Value: []byte("v")})
	if reason, _, ok := refusalFromStatus(status.Convert(err)); !ok || reason != replicaforward.ReasonNotAReplica || b.calls.Load() != 0 {
		t.Fatalf("Put after close = %v (%d target calls); want the refusal", err, b.calls.Load())
	}
}

func TestReplyType_OnlyRegisteredUnaryMethods(t *testing.T) {
	if f, ok := replyType(pb.QRDBService_Get_FullMethodName); !ok {
		t.Fatal("Get has no reply type")
	} else if _, isGet := f().(*pb.GetResponse); !isGet {
		t.Fatalf("Get reply = %T", f())
	}
	for _, m := range []string{pb.QRDBService_Watch_FullMethodName, "/qrdb.v1.QRDBService/Nope", "/nope.Service/Get", "/qrdb.v1.GetRequest/Get", "nomethod", "/"} {
		if _, ok := replyType(m); ok {
			t.Errorf("%q has a reply type; want none", m)
		}
	}
}

func TestForwardedMetadata_DropsCredentialsAndTransportKeys(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{
		"authorization": {"Bearer x"}, MetadataDatabase: {"orders"}, "grpc-timeout": {"1S"},
		":authority": {"h"}, "x-custom": {"a", "b"}, forward.MetadataAssertion: {"a"},
	})
	got := forwardedMetadata(ctx)
	if len(got) != 2 || got.Get(MetadataDatabase)[0] != "orders" || len(got.Get("x-custom")) != 2 {
		t.Fatalf("forwarded metadata = %v; want the scope and the custom key only", got)
	}
}

// TestForwardAdminWrite_ForwardsToLeaderAndRelaysTheAnswer is the node-parity
// regression guard (owner ruling 2026-10-06): a handler refused with a
// leader-only error (service.AdminCredentialNotLeaderError) resolves the
// named leader through the SAME member records the per-database
// NotAReplicaError path (forward) reads, signs the identical on-behalf
// assertion, and relays the leader's answer -- a single hop by address, with
// no placement or replica set involved.
func TestForwardAdminWrite_ForwardsToLeaderAndRelaysTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-1", "raft-leader")

	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: leader.addr}},
	}
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 5 * time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	reply, ferr, ok := fwd.forwardAdminWrite(ctx, pb.QRDBService_CeremonyInitialize_FullMethodName, &pb.CeremonyInitializeRequest{}, "raft-leader")
	if !ok {
		t.Fatal("forwardAdminWrite: not forwarded; want the leader's answer")
	}
	if ferr != nil {
		t.Fatalf("forwardAdminWrite: unexpected error %v; want the leader's success", ferr)
	}
	resp, ok := reply.(*pb.CeremonyInitializeResponse)
	if !ok || resp.GetSpkiPin() != "leader-answered" {
		t.Fatalf("reply = %+v; want the leader's CeremonyInitializeResponse", reply)
	}
	seen := leader.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" || seen.tenant != "acme" {
		t.Fatalf("leader saw %+v; want a verified assertion for alice/acme", seen)
	}
}

// TestForwardAdminWrite_UnresolvableLeaderDoesNotForward proves
// forwardAdminWrite falls back (ok=false) rather than forwarding when the
// named leader has no member record -- the caller then answers with its own
// refusal, exactly as REST's tryForwardWrite does on the same condition.
func TestForwardAdminWrite_UnresolvableLeaderDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	if _, _, ok := fwd.forwardAdminWrite(context.Background(), pb.QRDBService_CeremonyInitialize_FullMethodName, &pb.CeremonyInitializeRequest{}, "raft-unknown"); ok {
		t.Fatal("forwardAdminWrite forwarded to a leader with no member record")
	}
}

// TestForwardAdminWrite_EmptyLeaderAddressDoesNotForward proves
// forwardAdminWrite refuses to forward when the refusal names no leader at
// all, the same condition REST's tryForwardWrite refuses on.
func TestForwardAdminWrite_EmptyLeaderAddressDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	if _, _, ok := fwd.forwardAdminWrite(context.Background(), pb.QRDBService_CeremonyInitialize_FullMethodName, &pb.CeremonyInitializeRequest{}, ""); ok {
		t.Fatal("forwardAdminWrite forwarded with an empty leader address")
	}
}

// TestForwardAdminWrite_RelaysTheLeadersOwnError is the regression guard for
// the bug this gate caught: a leader that actually answered -- with its own
// application error, here the SO-PIN refusal CeremonyInitialize's admin-
// credential step gives -- must have that answer relayed to the original
// caller, not be treated as "not forwarded" and fall back to the follower's
// stale AdminCredentialNotLeaderError. Only a request that never reached the
// leader falls back.
func TestForwardAdminWrite_RelaysTheLeadersOwnError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-1", "raft-leader")
	leader.answer = func(context.Context) error {
		return status.Error(codes.Unauthenticated, "pin: invalid PIN")
	}

	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: leader.addr}},
	}
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 5 * time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	reply, ferr, ok := fwd.forwardAdminWrite(ctx, pb.QRDBService_CeremonyInitialize_FullMethodName, &pb.CeremonyInitializeRequest{}, "raft-leader")
	if !ok {
		t.Fatal("forwardAdminWrite: not forwarded; want the leader's answer relayed")
	}
	if reply != nil {
		t.Fatalf("reply = %+v; want nil alongside the leader's error", reply)
	}
	if status.Code(ferr) != codes.Unauthenticated {
		t.Fatalf("error = %v; want the leader's Unauthenticated refusal relayed, not a forwarding failure", ferr)
	}
}
