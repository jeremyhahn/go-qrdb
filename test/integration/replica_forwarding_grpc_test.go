// Copyright 2026 Jeremy Hahn
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

//go:build integration

package integration_test

// The replica-forwarding suite's gRPC client.

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// grpcFwdTransport is a gRPC client of a node's TCP listener or, with unix
// set, of its Unix socket, which serves the same services and forwards over
// gRPC.
type grpcFwdTransport struct {
	unix bool
}

func (g grpcFwdTransport) name() string {
	if g.unix {
		return "unix"
	}
	return "grpc"
}

func (grpcFwdTransport) hasStream() bool { return true }

func (grpcFwdTransport) reportsServedBy() bool { return true }

// client is a gRPC client of node i authenticated as caller.
func (g grpcFwdTransport) client(t *testing.T, c *fwdCluster, i int, caller fwdCaller) pb.QRDBServiceClient {
	t.Helper()
	certPath, keyPath := c.certFor(caller)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{RootCAs: c.ca.Pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	target := fmt.Sprintf("127.0.0.1:%d", c.nodes[i].grpc)
	if g.unix {
		target, cfg.ServerName = "unix://"+c.nodes[i].unix, "127.0.0.1"
	}
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return pb.NewQRDBServiceClient(cc)
}

// grpcDBCtx scopes a call to database db.
func grpcDBCtx(db string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	return metadata.AppendToOutgoingContext(ctx, "x-qrdb-database", db), cancel
}

// grpcAnswer is a gRPC answer in the suite's terms.
func grpcAnswer(err error, header metadata.MD) fwdAnswer {
	a := fwdAnswer{ok: err == nil, detail: fmt.Sprintf("%v", err)}
	if err == nil {
		a.detail = "OK"
	}
	if v := header.Get("x-qrdb-forwarded-to"); len(v) == 1 {
		a.servedBy = v[0]
	}
	a.denied = status.Code(err) == codes.PermissionDenied
	a.notFound = status.Code(err) == codes.NotFound
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetReason() == "NOT_A_REPLICA" && status.Code(err) == codes.Unavailable {
			a.refusal = info.GetMetadata()
		}
	}
	return a
}

func (g grpcFwdTransport) put(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key, value string) fwdAnswer {
	t.Helper()
	ctx, cancel := grpcDBCtx(db)
	defer cancel()
	var header metadata.MD
	_, err := g.client(t, c, node, caller).Put(ctx, &pb.PutRequest{Key: key, Value: []byte(value)}, grpc.Header(&header))
	return grpcAnswer(err, header)
}

func (g grpcFwdTransport) get(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key string) fwdAnswer {
	t.Helper()
	ctx, cancel := grpcDBCtx(db)
	defer cancel()
	var header metadata.MD
	resp, err := g.client(t, c, node, caller).Get(ctx, &pb.GetRequest{Key: key}, grpc.Header(&header))
	a := grpcAnswer(err, header)
	a.value = string(resp.GetValue())
	return a
}

func (g grpcFwdTransport) watch(t *testing.T, c *fwdCluster, node int, db string) fwdAnswer {
	t.Helper()
	ctx, cancel := grpcDBCtx(db)
	defer cancel()
	stream, err := g.client(t, c, node, fwdAsWriter).Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
	if err == nil {
		_, err = stream.Recv()
	}
	return grpcAnswer(err, nil)
}
