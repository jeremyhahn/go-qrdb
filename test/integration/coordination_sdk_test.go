//go:build integration

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

package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

func TestCoordinator_SameOnEveryTransport(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			client := coordinationClientForProtocol(t, node, proto)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.Connect(ctx); err != nil {
				t.Fatalf("[%s] connect: %v", proto.name, err)
			}
			coord, err := client.Coordination()
			if err != nil {
				t.Fatalf("[%s] coordination: %v", proto.name, err)
			}
			assertCoordinatorSurface(t, ctx, coord, proto.name)
		})
	}
}

func coordinationClientForProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) *sdk.Client {
	t.Helper()

	opts := []transport.Option{
		transport.WithMTLS(node.clientCert, node.clientKey, node.ca.CertPath),
	}
	var (
		tr  transport.Transport
		err error
	)
	switch proto.name {
	case "rest":
		tr, err = resttransport.New(append(opts, transport.WithAddress(node.serverURLFor(proto)))...)
	case "grpc":
		tr, err = grpctransport.New(append(opts, transport.WithAddress(fmt.Sprintf("127.0.0.1:%d", node.grpcPort)))...)
	case "quic":
		tr, err = quictransport.New(append(opts, transport.WithAddress(fmt.Sprintf("127.0.0.1:%d", node.quicPort)))...)
	case "mcp":
		tr, err = mcptransport.New(append(opts, transport.WithAddress(fmt.Sprintf("https://127.0.0.1:%d/mcp", node.mcpPort)))...)
	case "unix":
		tr, err = unixtransport.New(append(opts, transport.WithAddress(node.unixSocket))...)
	default:
		t.Fatalf("unknown protocol %q", proto.name)
	}
	if err != nil {
		t.Fatalf("[%s] transport: %v", proto.name, err)
	}
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("[%s] client: %v", proto.name, err)
	}
	return client
}

func assertCoordinatorSurface(t *testing.T, ctx context.Context, coord sdk.CoordinationClient, proto string) {
	t.Helper()

	prefix := fmt.Sprintf("coord/%s/%d", proto, time.Now().UnixNano())
	key := prefix + "/state"
	if got, err := coord.Get(ctx, key); err != nil || got != nil {
		t.Fatalf("[%s] missing Get = %q, %v; want nil, nil", proto, got, err)
	}
	if err := coord.Put(ctx, key, []byte("one")); err != nil {
		t.Fatalf("[%s] Put: %v", proto, err)
	}
	if got, err := coord.Get(ctx, key); err != nil || !bytes.Equal(got, []byte("one")) {
		t.Fatalf("[%s] Get = %q, %v; want one, nil", proto, got, err)
	}
	ok, err := coord.CompareAndSwap(ctx, prefix+"/claim", nil, []byte("owner-a"))
	if err != nil || !ok {
		t.Fatalf("[%s] create CAS = %v, %v; want true, nil", proto, ok, err)
	}
	ok, err = coord.CompareAndSwap(ctx, prefix+"/claim", nil, []byte("owner-b"))
	if err != nil || ok {
		t.Fatalf("[%s] losing CAS = %v, %v; want false, nil", proto, ok, err)
	}

	lease, err := coord.AcquireLease(ctx, prefix+"/leader", 5*time.Second)
	if err != nil {
		t.Fatalf("[%s] AcquireLease: %v", proto, err)
	}
	if lease.Key() != prefix+"/leader" {
		t.Fatalf("[%s] lease key = %q, want %q", proto, lease.Key(), prefix+"/leader")
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatalf("[%s] Release: %v", proto, err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := coord.Watch(watchCtx, prefix+"/watch/")
	if err != nil {
		t.Fatalf("[%s] Watch: %v", proto, err)
	}
	watchedKey := prefix + "/watch/item"
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-stream.Events():
			if !ok {
				t.Fatalf("[%s] watch ended early: %v", proto, stream.Err())
			}
			if ev.Type == sdk.EventPut && ev.Key == watchedKey && bytes.Equal(ev.Value, []byte("seen")) {
				cancel()
				return
			}
		case <-tick.C:
			if err := coord.Put(ctx, watchedKey, []byte("seen")); err != nil {
				t.Fatalf("[%s] watched Put: %v", proto, err)
			}
		case <-ctx.Done():
			t.Fatalf("[%s] no coordination watch event before deadline", proto)
		}
	}
}
