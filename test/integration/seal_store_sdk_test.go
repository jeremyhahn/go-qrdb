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
	"context"
	"errors"
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

// sealNodeAdminClient returns an SDK client for proto on node, authenticated
// with the admin credential `qrdb init` issued.
func sealNodeAdminClient(t *testing.T, node *sealNodeAllTransports, proto multiProtocol) *sdk.Client {
	t.Helper()
	opts := []transport.Option{transport.WithMTLS(node.adminCert, node.adminKey, node.caFile)}
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

// assertSealStoreSDKRefused calls every Client.SealStore() verb and requires
// each to fail with *transport.EmbeddedOnlyError naming the verb.
func assertSealStoreSDKRefused(t *testing.T, ctx context.Context, client *sdk.Client, proto multiProtocol, phase string) {
	t.Helper()
	store := client.SealStore()
	calls := map[string]func() error{
		transport.OpSealStorePut: func() error {
			return store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "alpha", Secret: []byte("seal-store-sdk-secret")})
		},
		transport.OpSealStoreGet: func() error {
			_, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
			return err
		},
		transport.OpSealStoreDelete: func() error {
			return store.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"})
		},
		transport.OpSealStoreList: func() error {
			_, err := store.SealStoreList(ctx)
			return err
		},
		transport.OpSealStoreReseal: func() error {
			return store.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"})
		},
		transport.OpSealStoreStatus: func() error {
			_, err := store.SealStoreStatus(ctx)
			return err
		},
	}
	for _, op := range transport.EmbeddedOnlyOperations {
		call, ok := calls[op]
		if !ok {
			t.Fatalf("[%s %s] no call for embedded-only operation %s", proto.name, phase, op)
		}
		err := call()
		if !errors.Is(err, transport.ErrEmbeddedOnly) {
			t.Fatalf("[%s %s] %s = %v, want transport.ErrEmbeddedOnly", proto.name, phase, op, err)
		}
		var eo *transport.EmbeddedOnlyError
		if !errors.As(err, &eo) || eo.Operation != op || eo.Transport != proto.name {
			t.Fatalf("[%s %s] %s = %v, want *transport.EmbeddedOnlyError{%s, %s}", proto.name, phase, op, err, op, proto.name)
		}
	}
}

// TestSealStore_SDK_EmbeddedOnly_PerProtocol: over every network transport
// each Client.SealStore() verb is refused as embedded-only (ruling 1234),
// while Client.Barrier().BarrierSeal and BarrierUnseal keep working on the
// same transport; the verbs stay refused after the seal/unseal cycle.
func TestSealStore_SDK_EmbeddedOnly_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node, _ := startEmbeddedOnlySealNode(t, proto)
			client := sealNodeAdminClient(t, node, proto)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := client.Connect(ctx); err != nil {
				t.Fatalf("[%s] connect: %v", proto.name, err)
			}

			assertSealStoreSDKRefused(t, ctx, client, proto, "unsealed")

			sealCtx, release, err := transport.WithSOPIN(ctx, []byte(sealNodeSOPIN))
			if err != nil {
				t.Fatalf("[%s] WithSOPIN: %v", proto.name, err)
			}
			err = client.Barrier().BarrierSeal(sealCtx)
			release()
			if err != nil {
				t.Fatalf("[%s] BarrierSeal: %v", proto.name, err)
			}
			if err := client.Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(embeddedOnlySealPassphrase)}); err != nil {
				t.Fatalf("[%s] BarrierUnseal: %v", proto.name, err)
			}
			status, err := client.Barrier().BarrierStatus(ctx)
			if err != nil {
				t.Fatalf("[%s] BarrierStatus after unseal: %v", proto.name, err)
			}
			if status.Sealed {
				t.Fatalf("[%s] BarrierStatus after unseal = %+v, want unsealed", proto.name, status)
			}

			assertSealStoreSDKRefused(t, ctx, client, proto, "after unseal")
		})
	}
}
