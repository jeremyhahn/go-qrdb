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
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fwdCA is the test cluster CA: it issues every node's listener certificate,
// which is also the client certificate the node forwards with.
type fwdCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newFwdCA(t *testing.T) *fwdCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fwd-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &fwdCA{cert: cert, key: key, pool: pool}
}

var fwdSerial atomic.Int64

// tlsFor returns a listener TLS config for a node (nodeID non-empty: a
// SPIFFE node SAN) or a user (nodeID empty), requiring client certificates
// from the same CA.
func (ca *fwdCA) tlsFor(t *testing.T, nodeID string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100 + fwdSerial.Add(1)), Subject: pkix.Name{CommonName: "peer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	if nodeID != "" {
		tmpl.URIs = []*url.URL{{Scheme: "spiffe", Host: "qrdb.cluster", Path: "/node/" + nodeID}}
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		RootCAs:      ca.pool,
		ClientCAs:    ca.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

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

func startTarget(t *testing.T, ca *fwdCA, nodeID, raftAddr string) *fakeTarget {
	t.Helper()
	ft := &fakeTarget{raftAddr: raftAddr}
	served := servedByUnary(nodeID)
	ft.addr = serveGRPC(t, ca.tlsFor(t, nodeID), ft, ft.record,
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

type fakeMembers struct{ members []cluster.Member }

func (m *fakeMembers) ListMembers(context.Context) ([]cluster.Member, error) { return m.members, nil }

// fwdCluster is an ingress node holding no replica and the replicas the
// placement names.
type fwdCluster struct {
	ca        *fwdCA
	targets   []*fakeTarget
	cache     *replicaforward.LeaderCache
	forwarder *replicaForwarder
	ingress   *refusingService
	client    pb.QRDBServiceClient
}

// newFwdCluster starts the ingress node, issued by ca, in front of the
// targets, which the placement names in order.
func newFwdCluster(t *testing.T, ca *fwdCA, timeout time.Duration, targets ...*fakeTarget) *fwdCluster {
	t.Helper()
	c := &fwdCluster{ca: ca, targets: targets, cache: replicaforward.NewLeaderCache()}
	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-a", LeaderReplicaID: 1}
	members := &fakeMembers{}
	for i, ft := range targets {
		refusal.Replicas = append(refusal.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: "node-" + strconv.Itoa(i+1), RaftAddr: ft.raftAddr})
		members.members = append(members.members, cluster.Member{NodeID: uint64(i + 1), Address: ft.raftAddr,
			APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: ft.addr}})
	}
	ingressTLS := c.ca.tlsFor(t, "node-a")
	c.forwarder = newReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil),
		RootCAs: c.ca.pool,
		Timeout: timeout,
	}, ingressTLS)
	t.Cleanup(c.forwarder.close)
	c.ingress = &refusingService{refusal: refusal}
	authn := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if v := metadata.ValueFromIncomingContext(ctx, "x-test-anonymous"); len(v) == 0 {
			ctx = auth.WithIdentity(ctx, &auth.Identity{Subject: "alice", TenantID: "acme"})
		}
		return h(ctx, req)
	}
	addr := serveGRPC(t, ingressTLS, c.ingress, authn, servedByUnary("node-a"), c.forwarder.unary)
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(c.ca.tlsFor(t, ""))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	c.client = pb.NewQRDBServiceClient(cc)
	return c
}

func callerCtx(pairs ...string) context.Context {
	md := metadata.Pairs(append([]string{MetadataDatabase, "orders", "authorization", "Bearer secret"}, pairs...)...)
	return metadata.NewOutgoingContext(context.Background(), md)
}

func TestReplicaForwardGRPC_ForwardsAsTheCallerAndRelaysTheAnswer(t *testing.T) {
	ca := newFwdCA(t)
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
	ca := newFwdCA(t)
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
	ca := newFwdCA(t)
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
	ca := newFwdCA(t)
	dead := &fakeTarget{raftAddr: "raft-b", addr: closedPort(t)}
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
	for name, setup := range map[string]func(b *fakeTarget){
		// Something between the nodes failed the request: the target's
		// handler never marked it served.
		"unmarked failure": func(b *fakeTarget) {
			b.unmarked = true
			b.answer = func(context.Context) error { return status.Error(codes.Internal, "on the way") }
		},
		// The target took the request and did not answer in time.
		"timeout": func(b *fakeTarget) {
			b.answer = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca := newFwdCA(t)
			b := startTarget(t, ca, "node-1", "raft-b")
			cc := startTarget(t, ca, "node-2", "raft-c")
			setup(b)
			c := newFwdCluster(t, ca, 300*time.Millisecond, b, cc)
			c.cache.Put(42, replicaforward.CachedLeader{TenantID: "acme", Database: "orders", ReplicaID: 1, RaftAddr: "raft-b",
				Endpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: b.addr}})

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
	ca := newFwdCA(t)
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
			ca := newFwdCA(t)
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
	ca := newFwdCA(t)
	b := startTarget(t, ca, "node-1", "raft-b")
	c := newFwdCluster(t, ca, 5*time.Second, b)
	c.forwarder.close()

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

// closedPort returns an address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
