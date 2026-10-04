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

// The replica-forwarding suite's native QUIC client. A QUIC answer frame
// has no metadata, so it cannot say which replica served a forward; a
// success through a node that holds no replica is the forward.

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

type quicFwdTransport struct{}

func (quicFwdTransport) name() string { return "quic" }

func (quicFwdTransport) hasStream() bool { return true }

func (quicFwdTransport) reportsServedBy() bool { return false }

// call sends one frame to node i as caller and returns the answer.
func (quicFwdTransport) call(t *testing.T, c *fwdCluster, i int, caller fwdCaller, method, db string, req proto.Message) (uint16, []byte, error) {
	t.Helper()
	certPath, keyPath := c.certFor(caller)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, fmt.Sprintf("127.0.0.1:%d", c.nodes[i].quic), &tls.Config{
		RootCAs: c.ca.Pool, Certificates: []tls.Certificate{cert}, NextProtos: []string{wire.ProtocolV3}, MinVersion: tls.VersionTLS13,
	}, &quicgo.Config{})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return 0, nil, err
	}
	if err := wire.WriteRequest(stream, method, db, 1, payload); err != nil {
		return 0, nil, err
	}
	_, st, body, err := wire.ReadResponse(stream)
	_ = stream.Close()
	return st, body, err
}

// quicAnswer is a QUIC answer in the suite's terms.
func quicAnswer(st uint16, body []byte, err error) fwdAnswer {
	if err != nil {
		return fwdAnswer{detail: err.Error()}
	}
	a := fwdAnswer{
		ok:       st == wire.StatusOK,
		denied:   st == wire.StatusPermissionDenied,
		notFound: st == wire.StatusNotFound,
		detail:   wire.StatusName(st),
	}
	if st != wire.StatusOK {
		if p, derr := wire.DecodeErrorPayload(body); derr == nil {
			a.detail += " " + p.Message
			if st == wire.StatusUnavailable && p.Info != nil && p.Info.Reason == "NOT_A_REPLICA" {
				a.refusal = p.Info.Metadata
			}
		}
	}
	return a
}

func (q quicFwdTransport) put(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key, value string) fwdAnswer {
	t.Helper()
	st, body, err := q.call(t, c, node, caller, "kv.Put", db, &pb.PutRequest{Key: key, Value: []byte(value)})
	return quicAnswer(st, body, err)
}

func (q quicFwdTransport) get(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key string) fwdAnswer {
	t.Helper()
	st, body, err := q.call(t, c, node, caller, "kv.Get", db, &pb.GetRequest{Key: key})
	a := quicAnswer(st, body, err)
	if a.ok {
		var resp pb.GetResponse
		if uerr := proto.Unmarshal(body, &resp); uerr != nil {
			t.Fatalf("decode kv.Get answer: %v", uerr)
		}
		a.value = string(resp.GetValue())
	}
	return a
}

func (q quicFwdTransport) watch(t *testing.T, c *fwdCluster, node int, db string) fwdAnswer {
	t.Helper()
	st, body, err := q.call(t, c, node, fwdAsWriter, "watch.Watch", db, &pb.WatchRequest{Key: []byte("k")})
	return quicAnswer(st, body, err)
}
