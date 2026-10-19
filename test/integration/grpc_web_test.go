//go:build integration

package integration_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// grpcWebCaller is one caller of TestGRPCWeb_SameAnswerAsNativeGRPC: a client
// certificate, a bearer token, both or neither.
type grpcWebCaller struct {
	certPath, keyPath string
	bearer            string
}

// tlsConfig is the caller's TLS configuration against node: the node's CA,
// and the caller's certificate when it has one.
func (c grpcWebCaller) tlsConfig(t *testing.T, node *multiProtoNode) *tls.Config {
	t.Helper()
	cfg := &tls.Config{RootCAs: node.ca.Pool, MinVersion: tls.VersionTLS13}
	if c.certPath != "" {
		cert, err := tls.LoadX509KeyPair(c.certPath, c.keyPath)
		if err != nil {
			t.Fatalf("load caller certificate: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

// nativePut is QRDBService/Put over the node's gRPC listener, in database db
// when db is set and in the caller's default database otherwise.
func (c grpcWebCaller) nativePut(t *testing.T, node *multiProtoNode, db string, req *pb.PutRequest) codes.Code {
	t.Helper()
	cc, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", node.grpcPort),
		grpc.WithTransportCredentials(credentials.NewTLS(c.tlsConfig(t, node))))
	if err != nil {
		t.Fatalf("gRPC client: %v", err)
	}
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if db != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-qrdb-database", db)
	}
	if c.bearer != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", c.bearer)
	}
	_, err = pb.NewQRDBServiceClient(cc).Put(ctx, req)
	return status.Code(err)
}

// webPut is the same call as gRPC-Web, under /grpc-web/ on the node's REST
// listener.
func (c grpcWebCaller) webPut(t *testing.T, node *multiProtoNode, db string, req *pb.PutRequest) codes.Code {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: c.tlsConfig(t, node), ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	base := fmt.Sprintf("https://127.0.0.1:%d/grpc-web", node.restPort)
	client := connectrpc.NewClient(connecthttp.NewTransport(&http.Client{Transport: tr}, base, connecthttp.WithGRPCWeb()))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx, info := connectrpc.NewClientContext(ctx)
	if db != "" {
		info.RequestHeader().Set("x-qrdb-database", db)
	}
	if c.bearer != "" {
		info.RequestHeader().Set("Authorization", c.bearer)
	}
	spec := connectrpc.Spec{StreamType: connectrpc.StreamTypeUnary, Procedure: pb.QRDBService_Put_FullMethodName}
	if err := client.CallUnary(ctx, spec, req, &pb.PutResponse{}); err != nil {
		return codes.Code(connectrpc.CodeOf(err))
	}
	return codes.OK
}

// TestGRPCWeb_SameAnswerAsNativeGRPC drives QRDBService/Put on one node with
// server.grpc_web enabled, through the native gRPC listener and through
// gRPC-Web on the REST listener side by side, and requires every caller to
// get the same status code from both: gRPC-Web is served by the gRPC
// server's own handler, so it runs the one interceptor chain and identifies a
// client certificate exactly as native gRPC does.
func TestGRPCWeb_SameAnswerAsNativeGRPC(t *testing.T) {
	node := startMultiProtocolNodeWithConfig(t, "", "server:\n  grpc_web:\n    enabled: true\n")
	rest := multiProtocols[0]

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	// provisionTenantIdentity binds one user per tenant, so the unprivileged
	// caller is the bound user of a third tenant, in its own default database.
	tenant, other, readers := "gwa-"+suffix, "gwb-"+suffix, "gwc-"+suffix
	db := "gwdb-" + suffix
	writer := []string{"databases:create", "databases:read", "kv:put"}
	createTenant(t, node, rest, tenant, "gRPC-Web A", "prefix", false)
	createTenant(t, node, rest, other, "gRPC-Web B", "prefix", false)
	createTenant(t, node, rest, readers, "gRPC-Web C", "prefix", false)
	writerCert, writerKey := provisionTenantIdentity(t, node, tenant, "gw-writer", writer)
	otherCert, otherKey := provisionTenantIdentity(t, node, other, "gw-writer", writer)
	readerCert, readerKey := provisionTenantIdentity(t, node, readers, "gw-reader", []string{"kv:get"})
	if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, rest, writerCert, writerKey), "database", "create", db, "--backend", "pebble"); err != nil {
		t.Fatalf("database create: %v\noutput: %s", err, out)
	}

	for _, tc := range []struct {
		name   string
		caller grpcWebCaller
		db     string
		want   codes.Code
	}{
		{name: "admin certificate admitted", caller: grpcWebCaller{certPath: node.clientCert, keyPath: node.clientKey}, want: codes.OK},
		{name: "tenant writer certificate admitted", caller: grpcWebCaller{certPath: writerCert, keyPath: writerKey}, db: db, want: codes.OK},
		{name: "no credentials refused", db: db, want: codes.Unauthenticated},
		{name: "bearer only refused", caller: grpcWebCaller{bearer: "Bearer not-issued-by-this-node"}, db: db, want: codes.Unauthenticated},
		// Another tenant's database does not exist for the caller at all.
		{name: "other tenant certificate refused", caller: grpcWebCaller{certPath: otherCert, keyPath: otherKey}, db: db, want: codes.NotFound},
		{name: "unprivileged bound user refused", caller: grpcWebCaller{certPath: readerCert, keyPath: readerKey}, want: codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &pb.PutRequest{Key: "gw/" + tc.name, Value: []byte("v")}
			native, web := tc.caller.nativePut(t, node, tc.db, req), tc.caller.webPut(t, node, tc.db, req)
			if native != tc.want || web != tc.want {
				t.Fatalf("native gRPC = %v, gRPC-Web = %v, want %v from both", native, web, tc.want)
			}
		})
	}
}
